//go:build !cli

package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"app/core"
	"app/internal/cli"
	"app/internal/gui"
	"app/internal/thumbnail"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

var title = "gotohp v" + getAppVersion()

func main() {
	// Run the CLI when the arguments name one of its commands; otherwise start the GUI.
	if cli.IsCLIInvocation(os.Args[1:]) {
		os.Exit(cli.Run(os.Args[1:], cli.Info{
			ExecutableName: "gotohp",
			HasGUI:         true,
			Version:        getAppVersion(),
		}))
	}

	runGUI()
}

func runGUI() {
	normalizeFrontendDevServerURL()

	configManager := &core.ConfigManager{}
	// Set once the app exists; thumbnails are only served for queued files.
	var uploadManager *core.UploadManager
	thumbnails := thumbnail.NewServer(func(path string) bool {
		return uploadManager != nil && uploadManager.HasItemPath(path)
	})
	wailsApp := application.New(application.Options{
		Name:        "com.xob0t.gotohp",
		Description: "Google Photos unofficial client",
		Services: []application.Service{
			application.NewService(configManager),
		},
		Assets: application.AssetOptions{
			Handler: application.BundledAssetFileServer(assets),
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/thumbnail" {
						thumbnails.ServeHTTP(w, r)
						return
					}
					next.ServeHTTP(w, r)
				})
			},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	window := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:          title,
		Frameless:      false,
		Width:          420,
		Height:         640,
		MinWidth:       380,
		MinHeight:      520,
		EnableFileDrop: true,
		BackgroundType: application.BackgroundTypeTranslucent,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	uploadManager = core.NewUploadManager(gui.NewWailsReporter(wailsApp), wailsApp.Logger)

	// Queue controls from the frontend
	wailsApp.Event.On("uploadCancel", func(e *application.CustomEvent) {
		uploadManager.Cancel()
	})
	wailsApp.Event.On("uploadPause", func(e *application.CustomEvent) {
		if paused, ok := e.Data.(bool); ok {
			uploadManager.SetPaused(paused)
		}
	})
	wailsApp.Event.On("uploadCancelItem", func(e *application.CustomEvent) {
		if id, ok := e.Data.(int64); ok {
			uploadManager.CancelItem(id)
		}
	})
	wailsApp.Event.On("uploadRetryItem", func(e *application.CustomEvent) {
		if id, ok := e.Data.(int64); ok {
			uploadManager.RetryItem(id)
		}
	})
	wailsApp.Event.On("uploadThreads", func(e *application.CustomEvent) {
		if threads, ok := e.Data.(int); ok {
			uploadManager.SetThreads(threads)
		}
	})
	wailsApp.Event.On("uploadRetryFailed", func(e *application.CustomEvent) {
		uploadManager.RetryFailed()
	})
	wailsApp.Event.On("uploadSkipped", func(e *application.CustomEvent) {
		uploadManager.UploadSkipped()
	})
	wailsApp.Event.On("uploadClearFinished", func(e *application.CustomEvent) {
		uploadManager.ClearFinished()
	})

	// On macOS a native view receives file drags, so the page gets no reliable
	// DOM drag events. Forward the native ones so the drop zones still show.
	// The "performed" event only schedules hiding: the drop target is read
	// from the page after it, so the zones must stay up until then.
	window.OnWindowEvent(events.Mac.WindowFileDraggingEntered, func(*application.WindowEvent) {
		wailsApp.Event.Emit("files-drag", true)
	})
	window.OnWindowEvent(events.Mac.WindowFileDraggingExited, func(*application.WindowEvent) {
		wailsApp.Event.Emit("files-drag", false)
	})
	window.OnWindowEvent(events.Mac.WindowFileDraggingPerformed, func(*application.WindowEvent) {
		wailsApp.Event.Emit("files-drag", false)
	})

	window.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		paths := event.Context().DroppedFiles()
		dropTarget := event.Context().DropTargetDetails()

		var dropZone string
		if dropTarget != nil {
			dropZone = dropTarget.Attributes["data-drop-zone"]
			wailsApp.Logger.Info("Drop target detected",
				"dropZone", dropZone,
				"elementID", dropTarget.ElementID)
		}

		// Emit event to frontend with drop details
		wailsApp.Event.Emit("files-dropped", core.FilesDroppedEvent{
			Files:    paths,
			DropZone: dropZone,
		})
	})

	// Listen for upload requests from the frontend (after the drop zone is
	// chosen). Files dropped during a run join the existing queue.
	wailsApp.Event.On("startUpload", func(e *application.CustomEvent) {
		if data, ok := e.Data.(core.StartUploadEvent); ok {
			wailsApp.Logger.Info("Queueing upload", "fileCount", len(data.Files))
			opts := configManager.SessionUploadOptions()
			opts.AlbumName = strings.TrimSpace(data.AlbumName)
			opts.AlbumAutoMode = data.AlbumAutoMode
			uploadManager.Upload(data.Files, opts)
		} else {
			wailsApp.Logger.Error("startUpload: unexpected data type", "type", fmt.Sprintf("%T", e.Data))
		}
	})

	err := wailsApp.Run()
	if err != nil {
		log.Fatal(err)
	}
}

func normalizeFrontendDevServerURL() {
	const envName = "FRONTEND_DEVSERVER_URL"

	value := os.Getenv(envName)
	value = strings.Replace(value, "http://localhost:", "http://127.0.0.1:", 1)
	value = strings.Replace(value, "https://localhost:", "https://127.0.0.1:", 1)
	if value != os.Getenv(envName) {
		_ = os.Setenv(envName, value)
	}
}
