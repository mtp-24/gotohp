//go:build darwin && cgo

package thumbnail

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework ImageIO -framework CoreGraphics -framework CoreMedia -framework AVFoundation -framework UniformTypeIdentifiers
#import <Foundation/Foundation.h>
#import <ImageIO/ImageIO.h>
#import <AVFoundation/AVFoundation.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <stdlib.h>
#include <string.h>

// Thumbnails are made in this process, not by the QuickLook service: that
// service cannot open files on external drives, where photo libraries often
// live.

// photoThumbnail uses ImageIO, which reads the preview embedded in camera
// HEIC and JPEG files and otherwise decodes at reduced size, never the full
// image.
static CGImageRef photoThumbnail(NSURL *url, int maxPixels) {
	CGImageSourceRef source = CGImageSourceCreateWithURL((__bridge CFURLRef)url, (__bridge CFDictionaryRef)@{
		(__bridge NSString *)kCGImageSourceShouldCache: @NO,
	});
	if (source == NULL) {
		return NULL;
	}
	CGImageRef image = CGImageSourceCreateThumbnailAtIndex(source, 0, (__bridge CFDictionaryRef)@{
		(__bridge NSString *)kCGImageSourceCreateThumbnailFromImageIfAbsent: @YES,
		(__bridge NSString *)kCGImageSourceCreateThumbnailWithTransform: @YES,
		(__bridge NSString *)kCGImageSourceThumbnailMaxPixelSize: @(maxPixels),
		(__bridge NSString *)kCGImageSourceShouldCacheImmediately: @NO,
	});
	CFRelease(source);
	return image;
}

// videoThumbnail grabs one frame near the start at thumbnail size, giving
// up after timeoutSeconds.
static CGImageRef videoThumbnail(NSURL *url, int maxPixels, double timeoutSeconds) {
	AVURLAsset *asset = [AVURLAsset URLAssetWithURL:url options:nil];
	AVAssetImageGenerator *generator = [AVAssetImageGenerator assetImageGeneratorWithAsset:asset];
	generator.appliesPreferredTrackTransform = YES;
	generator.maximumSize = CGSizeMake(maxPixels, maxPixels);
	generator.requestedTimeToleranceBefore = kCMTimePositiveInfinity;
	generator.requestedTimeToleranceAfter = kCMTimePositiveInfinity;
	if (@available(macOS 13.0, *)) {
		__block CGImageRef image = NULL;
		__block BOOL abandoned = NO;
		NSObject *lock = [NSObject new];
		dispatch_semaphore_t done = dispatch_semaphore_create(0);
		[generator generateCGImageAsynchronouslyForTime:CMTimeMake(0, 1)
			completionHandler:^(CGImageRef frame, CMTime actualTime, NSError *error) {
				@synchronized (lock) {
					if (!abandoned && frame != NULL) {
						image = CGImageRetain(frame);
					}
				}
				dispatch_semaphore_signal(done);
			}];
		long timedOut = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(timeoutSeconds * NSEC_PER_SEC)));
		@synchronized (lock) {
			if (timedOut != 0) {
				abandoned = YES;
				[generator cancelAllCGImageGeneration];
				return NULL;
			}
			return image;
		}
	}
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
	return [generator copyCGImageAtTime:CMTimeMake(0, 1) actualTime:NULL error:NULL];
#pragma clang diagnostic pop
}

// gotohpThumbnail makes a JPEG thumbnail at most maxPixels on its longer
// side. Returns 1 and sets *out (malloc'd) and *outLen on success.
static int gotohpThumbnail(const char *path, int maxPixels, double timeoutSeconds, void **out, int *outLen) {
	@autoreleasepool {
		NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
		UTType *type = [UTType typeWithFilenameExtension:url.pathExtension];
		CGImageRef image = NULL;
		if (type != nil && [type conformsToType:UTTypeAudiovisualContent]) {
			image = videoThumbnail(url, maxPixels, timeoutSeconds);
		} else {
			image = photoThumbnail(url, maxPixels);
		}
		if (image == NULL) {
			return 0;
		}

		NSMutableData *data = [NSMutableData data];
		CGImageDestinationRef destination = CGImageDestinationCreateWithData(
			(__bridge CFMutableDataRef)data, (__bridge CFStringRef)UTTypeJPEG.identifier, 1, NULL);
		if (destination == NULL) {
			CGImageRelease(image);
			return 0;
		}
		NSDictionary *properties = @{(__bridge NSString *)kCGImageDestinationLossyCompressionQuality: @0.7};
		CGImageDestinationAddImage(destination, image, (__bridge CFDictionaryRef)properties);
		BOOL ok = CGImageDestinationFinalize(destination);
		CFRelease(destination);
		CGImageRelease(image);
		if (!ok || data.length == 0) {
			return 0;
		}
		*outLen = (int)data.length;
		*out = malloc(data.length);
		if (*out == NULL) {
			return 0;
		}
		memcpy(*out, data.bytes, data.length);
		return 1;
	}
}
*/
import "C"

import (
	"time"
	"unsafe"
)

// Supported reports whether this platform can make thumbnails.
const Supported = true

func generate(path string, size int, timeout time.Duration) ([]byte, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var out unsafe.Pointer
	var outLen C.int
	// Twice the size in points, for Retina screens.
	if C.gotohpThumbnail(cPath, C.int(size*2), C.double(timeout.Seconds()), &out, &outLen) == 0 {
		return nil, errNoThumbnail
	}
	defer C.free(out)
	return C.GoBytes(out, outLen), nil
}
