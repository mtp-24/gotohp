import { Clipboard, Events } from "@wailsio/runtime";
import { markRaw, reactive } from "vue";
import {
  recordUploadResult,
  type UploadResults,
} from "./uploadResults";

export interface ThreadStatus {
  WorkerID: number;
  Status: string;
  FilePath: string;
  FileName: string;
  Message: string;
  BytesUploaded: number;
  BytesTotal: number;
  Attempt: number;
}

export interface FileUploadResult {
  MediaKey: string;
  IsError: boolean;
  IsLivePhoto: boolean;
  Skipped: boolean;
  ErrorMessage: string;
  SkipCode: string;
  SkipReason: string;
  Path: string;
  Paths: string[];
}

export interface PreflightWarning {
  Paths: string[];
  Code: string;
  Message: string;
}

export interface UploadBatchStart {
  Total: number;
  TotalBytes: number;
}

export interface AlbumStatus {
  AlbumName: string;
  ItemsAdded: number;
  TotalItems: number;
  AlbumKeys: string[];
  IsComplete: boolean;
}

export interface AlbumError {
  AlbumName: string;
  Error: string;
}

function isSkippedUploadWarning(code: string): boolean {
  return code === "incomplete-live-photo-skipped" || code === "ambiguous-filename-stem" || code === "ambiguous-identifier";
}

export type QueueItemState = "queued" | "active" | "done" | "failed" | "skipped" | "cancelled";

export interface QueueItem {
  ID: number;
  BatchID: number;
  Name: string;
  Path: string;
  Paths: string[];
  IsLivePhoto: boolean;
  Size: number;
  State: QueueItemState;
  Message: string;
  MediaKey: string;
  // Set on files skipped before upload, which can still be uploaded.
  SkipCode: string;
  // Frontend-only: the run the item last joined, for per-run progress.
  runId: number;
  // Frontend-only: rises each time the item is added or changes, so the list
  // can show the most recently changed items first.
  seq: number;
}

export interface QueueStatus {
  Paused: boolean;
  WaitingForNetwork: boolean;
  // Set when the queue stopped itself, e.g. because the account is full.
  StoppedReason: string;
  // End of a pause after HTTP 429, in Unix milliseconds, or 0.
  RateLimitedUntil: number;
  // Dropped files still being scanned; Phase is "" when none are.
  Scan: { Phase: string; Found: number; Checked: number; Total: number };
  // The library listing files are checked against before asking Google
  // about each: Phase is "listing", "ready", "failed" or "" without one.
  Library: { Phase: string; Items: number; Requests: number; Matched: number; Lookups: number };
}

// What a restored session brought back after the app restarted.
export interface SessionSummary {
  Restored: boolean;
  Finished: number;
  Remaining: number;
  Failed: number;
  AlbumFiles: number;
}

const idleQueueStatus = (): QueueStatus => ({
  Paused: false,
  WaitingForNetwork: false,
  StoppedReason: "",
  RateLimitedUntil: 0,
  Scan: { Phase: "", Found: 0, Checked: 0, Total: 0 },
  Library: { Phase: "", Items: 0, Requests: 0, Matched: 0, Lookups: 0 },
});

// One album this run adds to, and how far it got.
export interface AlbumEntry {
  name: string;
  added: number;
  total: number;
  state: "waiting" | "adding" | "done" | "failed";
  error: string;
}

// What one or more drops added, reported once their scan finishes.
export interface DropSummary {
  added: number;
  bytes: number;
  alreadyQueued: number;
  skipped: number;
  // Whether the drop joined a run that was already going.
  midRun: boolean;
  at: number;
}

export interface UploadState {
  isUploading: boolean;
  // Every item since the list was last cleared, in queue order. The array is
  // not reactive: with tens of thousands of items, re-rendering on every
  // change would cost too much. Read it together with listVersion, which
  // rises at most a few times a second after changes.
  items: QueueItem[];
  listVersion: number;
  // items.length as of the last listVersion.
  itemCount: number;
  // The latest drop's summary, set when its scan finishes.
  lastDrop: DropSummary | null;
  // Set when the queue saved before the app last quit was restored.
  restored: SessionSummary | null;
  runId: number;
  queueStatus: QueueStatus;
  totalFiles: number;
  uploadedFiles: number;
  threads: Map<number, ThreadStatus>;
  results: UploadResults;
  warnings: PreflightWarning[];
  // Byte tracking
  totalBytes: number;
  uploadedBytes: number;
  // Timing
  startTime: number;
  // Speed calculation (bytes per second)
  uploadSpeed: number;
  // Album creation
  albumStatus: AlbumStatus | null;
  isCreatingAlbum: boolean;
  // Every album of this run, in the order they were started.
  albums: AlbumEntry[];
}

class UploadManager {
  private static instance: UploadManager;

  // Reactive state that can be accessed by components
  public state = reactive<UploadState>({
    isUploading: false,
    items: markRaw([]) as QueueItem[],
    listVersion: 0,
    itemCount: 0,
    lastDrop: null,
    restored: null,
    runId: 0,
    queueStatus: idleQueueStatus(),
    totalFiles: 0,
    uploadedFiles: 0,
    threads: new Map<number, ThreadStatus>(),
    results: {
      success: [],
      fail: [],
      skipped: [],
      warnings: [],
    },
    warnings: [],
    totalBytes: 0,
    uploadedBytes: 0,
    startTime: 0,
    uploadSpeed: 0,
    albumStatus: null,
    isCreatingAlbum: false,
    albums: [],
  });

  // For speed calculation
  private lastSpeedUpdate: number = 0;
  private lastBytesUploaded: number = 0;
  private speedSamples: number[] = [];
  // Track bytes from completed files
  private completedBytes: number = 0;
  // Track the last known BytesTotal for each file
  private fileBytes: Map<string, number> = new Map();
  // Dedupe decisions can change the bytes that will actually be uploaded.
  private totalBytesAdjustment: number = 0;
  // Last value handed out as an item's seq.
  private seq = 0;
  // Counts what the drops being scanned add; see queueStatus.
  private drop: Omit<DropSummary, "at"> | null = null;
  // Throttles list refreshes; see refreshList.
  private listTimer: ReturnType<typeof setTimeout> | null = null;
  private lastListRefresh = 0;
  // Index into state.items by item ID.
  private itemIndex: Map<number, number> = new Map();

  private constructor() {
    // Bind all methods to ensure 'this' context is preserved
    this.resetUploadResults = this.resetUploadResults.bind(this);
    this.cancelUpload = this.cancelUpload.bind(this);
    this.copyResultsAsJson = this.copyResultsAsJson.bind(this);
    this.clearFinished = this.clearFinished.bind(this);

    this.setupEventListeners();
  }

  public static getInstance(): UploadManager {
    if (!UploadManager.instance) {
      UploadManager.instance = new UploadManager();
    }
    return UploadManager.instance;
  }

  private setupEventListeners() {
    // Handle upload start
    // Sent when a run begins and again whenever files join it; Total is the
    // run's running item count. Only the first one resets per-run counters.
    Events.On("uploadStart", (event: { data: UploadBatchStart }) => {
      if (!this.state.isUploading) {
        this.state.runId++;
        this.state.totalBytes = 0;
        this.state.uploadedFiles = 0;
        this.state.uploadedBytes = 0;
        this.state.isUploading = true;
        this.state.threads.clear();
        this.state.startTime = Date.now();
        this.state.uploadSpeed = 0;
        this.state.albumStatus = null;
        this.state.albums = [];
        this.state.queueStatus = idleQueueStatus();
        this.lastSpeedUpdate = Date.now();
        this.lastBytesUploaded = 0;
        this.speedSamples = [];
        this.completedBytes = 0;
        this.fileBytes.clear();
        this.totalBytesAdjustment = 0;
      }
      this.state.totalFiles = event.data.Total;
    });

    Events.On("queueItems", (event: { data: QueueItem[] }) => {
      for (const item of event.data) {
        // An update for the same item may already have arrived.
        if (!this.itemIndex.has(item.ID)) {
          this.insertItem(item);
        }
        if (this.drop) {
          if (item.State === "skipped") {
            this.drop.skipped++;
          } else {
            this.drop.added++;
            this.drop.bytes += Math.max(0, item.Size);
          }
        }
      }
    });

    // Changes made together (e.g. cancelling thousands of queued items)
    // arrive as one event and are applied in one pass.
    Events.On("queueItemsUpdated", (event: { data: QueueItem[] }) => {
      const items = this.state.items;
      const added: QueueItem[] = [];
      for (const update of event.data) {
        const index = this.itemIndex.get(update.ID);
        if (index === undefined) {
          added.push(update);
          continue;
        }
        const runId = update.State === "queued" || update.State === "active"
          ? this.state.runId
          : items[index].runId;
        items[index] = markRaw({ ...update, runId, seq: ++this.seq });
      }
      for (const item of added) {
        this.insertItem(item);
      }
      this.refreshList();
    });

    // A scan starting and finishing brackets what a drop adds: count it
    // between the two and publish a summary at the end.
    Events.On("queueStatus", (event: { data: QueueStatus }) => {
      const wasScanning = !!this.state.queueStatus.Scan?.Phase;
      const scanning = !!event.data.Scan?.Phase;
      if (!wasScanning && scanning) {
        this.drop = {
          added: 0, bytes: 0, alreadyQueued: 0, skipped: 0,
          midRun: this.state.isUploading && this.state.items.length > 0,
        };
      }
      this.state.queueStatus = event.data;
      if (wasScanning && !scanning && this.drop) {
        this.state.lastDrop = { ...this.drop, at: Date.now() };
        this.drop = null;
      }
    });

    // Handle async total bytes update (calculated after uploadStart)
    Events.On("uploadTotalBytes", (event: { data: number }) => {
      this.state.totalBytes = Math.max(0, event.data + this.totalBytesAdjustment);
    });

    Events.On("uploadTotalBytesDelta", (event: { data: number }) => {
      this.totalBytesAdjustment += event.data;
      this.state.totalBytes = Math.max(0, this.state.totalBytes + event.data);
    });

    Events.On("uploadWarning", (event: { data: PreflightWarning }) => {
      this.state.warnings.push(event.data);
      if (this.drop && event.data.Code === "already-queued") {
        this.drop.alreadyQueued += event.data.Paths.length;
      }
      if (!isSkippedUploadWarning(event.data.Code)) {
        this.state.results.warnings.push({
          paths: event.data.Paths,
          code: event.data.Code,
          reason: event.data.Message,
        });
      }
    });

    // Handle thread status updates
    Events.On("ThreadStatus", (event: { data: ThreadStatus }) => {
      const thread = event.data;
      const prevThread = this.state.threads.get(thread.WorkerID);

      if (thread.Status === 'uploading' && thread.FilePath && thread.BytesTotal > 0) {
        this.fileBytes.set(thread.FilePath, thread.BytesTotal);
      }

      if (thread.Status === 'error' && prevThread?.Message !== thread.Message) {
        window.dispatchEvent(new CustomEvent('uploadError', { detail: thread }));
      }
      
      this.state.threads.set(thread.WorkerID, thread);
      this.updateBytesAndSpeed();
    });

    // Handle file status updates
    Events.On("FileStatus", (event: { data: FileUploadResult }) => {
      const { Path, Paths } = event.data;

      this.state.uploadedFiles += recordUploadResult(this.state.results, event.data);
      if (!event.data.IsError) {
        const completedFileBytes = this.fileBytes.get(Path);
        if (completedFileBytes && completedFileBytes > 0) {
          this.completedBytes += completedFileBytes;
        }
      }
      for (const completedPath of Paths?.length ? Paths : [Path]) {
        this.fileBytes.delete(completedPath);
      }
      this.updateBytesAndSpeed();
    });

    // Handle upload stop
    Events.On("uploadStop", () => {
      this.state.isUploading = false;
    });

    // Album progress: an album is announced with nothing added while existing
    // albums are looked up, then reports each batch added.
    Events.On("albumProgress", (event: { data: AlbumStatus }) => {
      this.state.albumStatus = event.data;
      const entry = this.albumEntry(event.data.AlbumName, event.data.TotalItems);
      entry.added = event.data.ItemsAdded;
      entry.total = event.data.TotalItems || entry.total;
      entry.state = event.data.ItemsAdded > 0 ? "adding" : "waiting";
      this.updateAlbumFlag();
    });

    // An album is complete. Albums split at the item limit report progress as
    // "Name (1)", "Name (2)" but complete under "Name", so finish those too.
    Events.On("albumComplete", (event: { data: AlbumStatus }) => {
      this.state.albumStatus = event.data;
      const name = event.data.AlbumName;
      const parts = this.state.albums.filter(album => album.name === name || album.name.startsWith(`${name} (`));
      if (!parts.length) parts.push(this.albumEntry(name, event.data.TotalItems));
      for (const album of parts) {
        album.state = "done";
        album.added = album.total;
      }
      this.updateAlbumFlag();
    });

    Events.On("albumError", (event: { data: AlbumError }) => {
      const entry = this.albumEntry(event.data.AlbumName, 0);
      entry.state = "failed";
      entry.error = event.data.Error;
      this.updateAlbumFlag();
      // Emit a custom event that App.vue can listen to for showing toast
      window.dispatchEvent(new CustomEvent('albumError', { detail: event.data }));
    });

    Events.On("sessionRestored", (event: { data: SessionSummary }) => {
      this.state.restored = event.data;
    });
    // Asked for once the listeners above are in place, so the restored
    // queue's events are not missed.
    Events.Emit("restoreSession");
  }

  private updateBytesAndSpeed() {
    // Calculate bytes from currently active uploads
    let activeUploadedBytes = 0;

    this.state.threads.forEach((thread) => {
      if (thread.Status === 'uploading' && thread.BytesTotal > 0) {
        activeUploadedBytes += thread.BytesUploaded;
      } else if (thread.Status === 'finalizing' && thread.FilePath) {
        activeUploadedBytes += this.fileBytes.get(thread.FilePath) ?? 0;
      }
    });

    // Total uploaded = completed files + current progress
    const totalUploaded = this.completedBytes + activeUploadedBytes;
    this.state.uploadedBytes = totalUploaded;

    // Calculate speed (using rolling average)
    const now = Date.now();
    const timeDelta = now - this.lastSpeedUpdate;

    if (timeDelta >= 500) { // Update speed every 500ms
      const bytesDelta = totalUploaded - this.lastBytesUploaded;
      const instantSpeed = (bytesDelta / timeDelta) * 1000; // bytes per second

      if (instantSpeed >= 0) {
        this.speedSamples.push(instantSpeed);
        // Keep last 5 samples for smoothing
        if (this.speedSamples.length > 5) {
          this.speedSamples.shift();
        }
        // Calculate average speed
        this.state.uploadSpeed = this.speedSamples.reduce((a, b) => a + b, 0) / this.speedSamples.length;
      }

      this.lastSpeedUpdate = now;
      this.lastBytesUploaded = totalUploaded;
    }
  }

  public resetUploadResults() {
    this.state.results.success = [];
    this.state.results.fail = [];
    this.state.results.skipped = [];
    this.state.results.warnings = [];
  }

  private albumEntry(name: string, total: number): AlbumEntry {
    let entry = this.state.albums.find(album => album.name === name);
    if (!entry) {
      this.state.albums.push({ name, added: 0, total, state: "waiting", error: "" });
      entry = this.state.albums[this.state.albums.length - 1];
    }
    return entry;
  }

  private updateAlbumFlag() {
    this.state.isCreatingAlbum = this.state.albums.some(album => album.state === "waiting" || album.state === "adding");
  }

  private insertItem(item: QueueItem) {
    this.itemIndex.set(item.ID, this.state.items.length);
    // Items are replaced, never mutated, so they need no reactivity either.
    this.state.items.push(markRaw({ ...item, runId: this.state.runId, seq: ++this.seq }));
    this.refreshList();
  }

  // refreshList tells the page the list changed: at once after a quiet spell,
  // otherwise at most every 250 ms, however many items changed meanwhile.
  private refreshList() {
    if (this.listTimer) return;
    const wait = Math.max(0, 250 - (Date.now() - this.lastListRefresh));
    this.listTimer = setTimeout(() => {
      this.listTimer = null;
      this.lastListRefresh = Date.now();
      this.state.itemCount = this.state.items.length;
      this.state.listVersion++;
    }, wait);
  }

  public cancelUpload() {
    Events.Emit("uploadCancel");
  }

  public setPaused(paused: boolean) {
    Events.Emit("uploadPause", paused);
  }

  public cancelItem(id: number) {
    Events.Emit("uploadCancelItem", id);
  }

  public retryItem(id: number) {
    Events.Emit("uploadRetryItem", id);
  }

  public retryFailed() {
    Events.Emit("uploadRetryFailed");
  }

  // Uploads files that preflight skipped, such as incomplete Live Photos, as
  // ordinary files.
  public uploadSkipped() {
    Events.Emit("uploadSkipped");
  }

  // Drops finished items from the list, and their results, keeping anything
  // still queued or uploading.
  public clearFinished() {
    Events.Emit("uploadClearFinished");
    const remaining = this.state.items.filter(item => item.State === "queued" || item.State === "active");
    this.state.items = markRaw(remaining);
    this.itemIndex = new Map(remaining.map((item, index) => [item.ID, index]));
    this.refreshList();
    this.resetUploadResults();
    this.state.warnings = [];
    if (!this.state.isUploading) {
      this.state.uploadedFiles = 0;
      this.state.totalFiles = 0;
      this.state.albumStatus = null;
      this.state.albums = [];
    }
  }

  public async copyResultsAsJson() {
    const resultsJson = JSON.stringify(this.state.results, null, 2);
    try {
      await Clipboard.SetText(resultsJson);
      console.log("Upload results copied to clipboard");
      return true;
    } catch (error) {
      console.error("Failed to copy results:", error);
      return false;
    }
  }
}

// Create and export a single instance
export const uploadManager = UploadManager.getInstance();
