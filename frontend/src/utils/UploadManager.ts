import { Clipboard, Events } from "@wailsio/runtime";
import { reactive } from "vue";
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
  return code === "incomplete-live-photo-skipped" || code === "ambiguous-filename-stem";
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
  // Frontend-only: the run the item last joined, for per-run progress.
  runId: number;
}

export interface QueueStatus {
  Paused: boolean;
  WaitingForNetwork: boolean;
}

export interface UploadState {
  isUploading: boolean;
  // Every item since the list was last cleared, in queue order.
  items: QueueItem[];
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
}

class UploadManager {
  private static instance: UploadManager;

  // Reactive state that can be accessed by components
  public state = reactive<UploadState>({
    isUploading: false,
    items: [],
    runId: 0,
    queueStatus: { Paused: false, WaitingForNetwork: false },
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
        this.state.queueStatus = { Paused: false, WaitingForNetwork: false };
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
      }
    });

    Events.On("queueItemUpdate", (event: { data: QueueItem }) => {
      const index = this.itemIndex.get(event.data.ID);
      if (index === undefined) {
        this.insertItem(event.data);
        return;
      }
      const runId = event.data.State === "queued" || event.data.State === "active"
        ? this.state.runId
        : this.state.items[index].runId;
      this.state.items[index] = { ...event.data, runId };
    });

    Events.On("queueStatus", (event: { data: QueueStatus }) => {
      this.state.queueStatus = event.data;
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

    // Handle album creation progress
    Events.On("albumProgress", (event: { data: AlbumStatus }) => {
      this.state.albumStatus = event.data;
      this.state.isCreatingAlbum = true;
    });

    // Handle album creation complete
    Events.On("albumComplete", (event: { data: AlbumStatus }) => {
      this.state.albumStatus = event.data;
      this.state.isCreatingAlbum = false;
    });

    // Handle album creation error
    Events.On("albumError", (event: { data: AlbumError }) => {
      this.state.isCreatingAlbum = false;
      // Emit a custom event that App.vue can listen to for showing toast
      window.dispatchEvent(new CustomEvent('albumError', { detail: event.data }));
    });
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

  private insertItem(item: QueueItem) {
    this.itemIndex.set(item.ID, this.state.items.length);
    this.state.items.push({ ...item, runId: this.state.runId });
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

  // Drops finished items from the list, and their results, keeping anything
  // still queued or uploading.
  public clearFinished() {
    Events.Emit("uploadClearFinished");
    const remaining = this.state.items.filter(item => item.State === "queued" || item.State === "active");
    this.state.items = remaining;
    this.itemIndex = new Map(remaining.map((item, index) => [item.ID, index]));
    this.resetUploadResults();
    this.state.warnings = [];
    if (!this.state.isUploading) {
      this.state.uploadedFiles = 0;
      this.state.totalFiles = 0;
      this.state.albumStatus = null;
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
