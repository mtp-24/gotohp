package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"app/generated"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// PhotosCreateMediaItems is the shared commit RPC observed for both ordinary
// images and linked Live Photos; pairing changes the request body, not the RPC.
const photosCreateMediaItemsEndpoint = "https://photosdata-pa.googleapis.com/6439526531001121323/16538846908252377752"

type Api struct {
	androidAPIVersion int64
	model             string
	make              string
	clientVersionCode int64
	userAgent         string
	language          string
	authData          string
	client            *http.Client
	authResponseCache map[string]string
	commitEndpoint    string
	authEndpoint      string
	commitRetryConfig *RetryConfig
	saver             bool
	useQuota          bool
}

type AuthResponse struct {
	Expiry string
	Auth   string
}

// ApiOptions selects the account and per-run upload policy for an API client.
type ApiOptions struct {
	// Account is the credential email to use; empty means the selected account.
	Account  string
	Proxy    string
	Saver    bool
	UseQuota bool
}

// NewApi creates a client for the account in opts using the loaded credential store.
func NewApi(opts ApiOptions) (*Api, error) {
	account := currentConfig().Account
	email := opts.Account
	if email == "" {
		email = account.Selected
	}
	if email == "" {
		return nil, fmt.Errorf("no account is selected")
	}
	credentials := ""
	for _, c := range account.Credentials {
		params, err := url.ParseQuery(c)
		if err != nil {
			continue
		}
		if strings.EqualFold(params.Get("Email"), email) {
			credentials = c
		}
	}
	if len(credentials) == 0 {
		return nil, fmt.Errorf("no credentials found for account %s", email)
	}

	api, err := newAPIFromCredential(credentials, opts.Proxy)
	if err != nil {
		return nil, err
	}
	api.saver = opts.Saver
	api.useQuota = opts.UseQuota
	return api, nil
}

func newAPIFromCredential(credentials string, proxy string) (*Api, error) {
	params, err := url.ParseQuery(credentials)
	if err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	client, err := NewHTTPClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	api := &Api{
		androidAPIVersion: 28,
		model:             "Pixel XL",
		make:              "Google",
		clientVersionCode: 49029607,
		language:          params.Get("lang"),
		authData:          strings.TrimSpace(credentials),
		client:            client,
		authResponseCache: map[string]string{
			"Expiry": "0",
			"Auth":   "",
		},
	}

	api.userAgent = fmt.Sprintf(
		"com.google.android.apps.photos/%d (Linux; U; Android 9; %s; %s; Build/PQ2A.190205.001; Cronet/127.0.6510.5) (gzip)",
		api.clientVersionCode,
		api.language,
		api.model,
	)

	return api, nil
}

func (a *Api) BearerToken() (string, error) {
	expiryStr := a.authResponseCache["Expiry"]
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry time: %w", err)
	}

	if expiry <= time.Now().Unix() {
		resp, err := a.getAuthToken()
		if err != nil {
			return "", fmt.Errorf("failed to get auth token: %w", err)
		}
		a.authResponseCache = resp
	}

	if token, ok := a.authResponseCache["Auth"]; ok && token != "" {
		return token, nil
	}

	return "", errors.New("auth response does not contain bearer token")
}

func (a *Api) getAuthToken() (map[string]string, error) {
	authDataValues, err := url.ParseQuery(a.authData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse auth data: %w", err)
	}

	authRequestData := url.Values{}
	for key, values := range authDataValues {
		authRequestData[key] = append([]string(nil), values...)
	}
	authRequestData.Set("app", "com.google.android.apps.photos")
	authRequestData.Set("callerPkg", "com.google.android.apps.photos")
	authRequestData.Del("it_caveat_types")
	authRequestData.Del("assertion_jwt")

	var tokenBinding *tokenBindingSession
	if alias := authRequestData.Get("token_binding_alias"); alias != "" {
		var assertionJWT string
		tokenBinding, assertionJWT, err = newTokenBindingSession(alias)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare token binding assertion: %w", err)
		}
		authRequestData.Set("assertion_jwt", assertionJWT)
	}
	authRequestData.Del("token_binding_alias")

	headers := map[string]string{
		"Accept-Encoding": "gzip",
		"app":             "com.google.android.apps.photos",
		"Connection":      "Keep-Alive",
		"Content-Type":    "application/x-www-form-urlencoded",
		"device":          authRequestData.Get("androidId"),
		"User-Agent":      "GoogleAuth/1.4 (Pixel XL PQ2A.190205.001); gzip",
	}

	authEndpoint := a.authEndpoint
	if authEndpoint == "" {
		authEndpoint = "https://android.googleapis.com/auth"
	}
	req, err := http.NewRequest("POST", authEndpoint, strings.NewReader(authRequestData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Check for errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return make(map[string]string), newAPIError(resp)
	}

	// Parse the response body
	bodyBytes, err := ReadResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse the key=value response format
	parsedAuthResponse := make(map[string]string)
	for _, line := range strings.Split(string(bodyBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			parsedAuthResponse[parts[0]] = parts[1]
		}
	}
	// A dead master token can come back as HTTP 200 with an Error= line.
	if authErr := parsedAuthResponse["Error"]; authErr != "" {
		return nil, &APIError{StatusCode: resp.StatusCode, Code: rpcCodeUnauthenticated, Message: "Google sign-in failed: " + authErr}
	}
	if err := decryptTokenEncryptedResponse(parsedAuthResponse, tokenBinding); err != nil {
		return nil, err
	}

	// Validate we got the required fields
	if parsedAuthResponse["Auth"] == "" {
		return nil, errors.New("auth response missing Auth token")
	}
	if parsedAuthResponse["Expiry"] == "" {
		return nil, errors.New("auth response missing Expiry")
	}

	return parsedAuthResponse, nil
}

// send performs a request authenticated with the account's bearer token.
// When Google answers 401 or 403 the cached token is dropped and the request
// is sent once more with a fresh one, since an expired token looks the same
// as a revoked credential.
func (a *Api) send(newRequest func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		bearerToken, err := a.BearerToken()
		if err != nil {
			return nil, fmt.Errorf("failed to get bearer token: %w", err)
		}
		req, err := newRequest()
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+bearerToken)
		resp, err := a.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		if attempt == 0 && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			_ = resp.Body.Close()
			a.invalidateBearerToken()
			continue
		}
		return resp, nil
	}
}

func (a *Api) invalidateBearerToken() {
	a.authResponseCache = map[string]string{"Expiry": "0", "Auth": ""}
}

func newPostRequest(url string, body []byte, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Obtain a file upload token from the Google Photos API.
func (a *Api) GetUploadToken(shaHashB64 string, fileSize int64) (string, error) {
	// Create the protobuf message
	protoBody := generated.GetUploadToken{
		F1:            2,
		F2:            2,
		F3:            1,
		F4:            3,
		FileSizeBytes: fileSize,
	}

	// Serialize the protobuf message
	serializedData, err := proto.Marshal(&protoBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf: %w", err)
	}

	// Prepare headers
	headers := map[string]string{
		"Accept-Encoding":         "gzip",
		"Accept-Language":         a.language,
		"Content-Type":            "application/x-protobuf",
		"User-Agent":              a.userAgent,
		"X-Goog-Hash":             "sha1=" + shaHashB64,
		"X-Upload-Content-Length": strconv.Itoa(int(fileSize)),
	}

	resp, err := a.send(func() (*http.Request, error) {
		return newPostRequest("https://photos.googleapis.com/data/upload/uploadmedia/interactive", serializedData, headers)
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// Check for errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp)
	}

	// Get the upload token from headers
	uploadToken := resp.Header.Get("X-GUploader-UploadID")
	if uploadToken == "" {
		return "", errors.New("response missing X-GUploader-UploadID header")
	}

	return uploadToken, nil
}

// hashCheckEndpoint looks up library items by SHA-1.
const hashCheckEndpoint = "https://photosdata-pa.googleapis.com/6439526531001121323/5084965799730810217"

// FindRemoteMediaByHash returns the media key of a library item with this
// SHA-1, or "" when there is none.
func (a *Api) FindRemoteMediaByHash(shaHash []byte) (string, error) {
	protoBody := generated.HashCheck{
		Field1: &generated.HashCheckField1Type{
			Field1: &generated.HashCheckField1TypeField1Type{
				Sha1Hash: shaHash,
			},
			Field2: &generated.HashCheckField1TypeField2Type{},
		},
	}
	serializedData, err := proto.Marshal(&protoBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf: %w", err)
	}
	return a.findRemoteMedia(serializedData)
}

// FindLivePhotoMotionByHash looks up a video SHA-1 the way the Google Photos
// app checks Live Photo motion: field 5 = 2 marks the query as a Live Photo
// movie, so it only matches once that video is attached to a still. The
// generated HashCheck message has no field 5, so the request is built by hand:
// {1: {1: {1: sha1, 5: 2}, 2: {}}}.
func (a *Api) FindLivePhotoMotionByHash(shaHash []byte) (string, error) {
	var query []byte
	query = protowire.AppendTag(query, 1, protowire.BytesType)
	query = protowire.AppendBytes(query, shaHash)
	query = protowire.AppendTag(query, 5, protowire.VarintType)
	query = protowire.AppendVarint(query, 2)

	var lookup []byte
	lookup = protowire.AppendTag(lookup, 1, protowire.BytesType)
	lookup = protowire.AppendBytes(lookup, query)
	lookup = protowire.AppendTag(lookup, 2, protowire.BytesType)
	lookup = protowire.AppendBytes(lookup, nil)

	var body []byte
	body = protowire.AppendTag(body, 1, protowire.BytesType)
	body = protowire.AppendBytes(body, lookup)
	return a.findRemoteMedia(body)
}

func (a *Api) findRemoteMedia(serializedData []byte) (string, error) {
	// The x-goog-ext headers the commit RPCs send must not be added here; the
	// lookup rejects them with HTTP 400.
	headers := map[string]string{
		"Accept-Encoding": "gzip",
		"Accept-Language": a.language,
		"Content-Type":    "application/x-protobuf",
		"User-Agent":      a.userAgent,
	}

	resp, err := a.send(func() (*http.Request, error) {
		return newPostRequest(hashCheckEndpoint, serializedData, headers)
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp)
	}

	bodyBytes, err := ReadResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var pbResp generated.RemoteMatches
	if err := proto.Unmarshal(bodyBytes, &pbResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}
	return pbResp.GetMediaKey(), nil
}

// UploadProgressCallback is called with progress updates during file upload
// attempt is 1-based (1 = first attempt, 2 = first retry, etc.)
type UploadProgressCallback func(bytesUploaded, bytesTotal int64, attempt int)

func (a *Api) UploadFile(ctx context.Context, filePath string, uploadToken string) (ScottyFinalizeToken, error) {
	return a.UploadFileWithProgress(ctx, filePath, uploadToken, nil)
}

// UploadFileWithProgress uploads a file to the session uploadToken, retrying
// the same session on transient failures.
func (a *Api) UploadFileWithProgress(ctx context.Context, filePath string, uploadToken string, onProgress UploadProgressCallback) (ScottyFinalizeToken, error) {
	return a.uploadFile(ctx, filePath, uploadToken, nil, onProgress)
}

// UploadFileWithNewSessions is UploadFileWithProgress, but each retry starts
// a new upload session from newToken: a session that failed partway through
// may not accept the file again from the start.
func (a *Api) UploadFileWithNewSessions(ctx context.Context, filePath string, uploadToken string, newToken func() (string, error), onProgress UploadProgressCallback) (ScottyFinalizeToken, error) {
	return a.uploadFile(ctx, filePath, uploadToken, newToken, onProgress)
}

func (a *Api) uploadFile(ctx context.Context, filePath string, uploadToken string, newToken func() (string, error), onProgress UploadProgressCallback) (ScottyFinalizeToken, error) {
	// Get file size first (needed for progress tracking)
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return ScottyFinalizeToken{}, fmt.Errorf("error getting file info: %w", err)
	}
	fileSize := fileInfo.Size()

	const uploadURLPrefix = "https://photos.googleapis.com/data/upload/uploadmedia/interactive?upload_id="
	uploadURL := uploadURLPrefix + uploadToken
	retryConfig := DefaultRetryConfig()

	var lastErr error
	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		attemptNum := attempt + 1 // 1-based for display

		// Check context before each attempt
		if ctx.Err() != nil {
			return ScottyFinalizeToken{}, ctx.Err()
		}

		// Wait before retry (skip on first attempt)
		if attempt > 0 {
			delay := CalculateBackoff(attempt-1, retryConfig)
			select {
			case <-ctx.Done():
				return ScottyFinalizeToken{}, ctx.Err()
			case <-time.After(delay):
			}
			if newToken != nil {
				token, err := newToken()
				if err != nil {
					lastErr = fmt.Errorf("error starting a new upload session: %w", err)
					continue
				}
				uploadURL = uploadURLPrefix + token
			}
		}

		// Signal start of this attempt (resets progress on retry)
		if onProgress != nil {
			onProgress(0, fileSize, attemptNum)
		}

		// Open file fresh for each attempt - this is the key to not loading into memory
		file, err := os.Open(filePath)
		if err != nil {
			return ScottyFinalizeToken{}, fmt.Errorf("error opening file: %w", err)
		}

		reader := NewProgressReader(file, fileSize, func(bytesRead, total int64) {
			if onProgress != nil {
				onProgress(bytesRead, total, attemptNum)
			}
		})

		// Abandon the attempt if the connection stops taking data.
		attemptCtx, cancelAttempt := context.WithCancelCause(ctx)
		stopWatch := watchUploadProgress(reader, fileSize, uploadStallTimeout, func() {
			cancelAttempt(errUploadStalled)
		})
		result, retryable, err := a.doUploadRequest(attemptCtx, uploadURL, reader)
		stopWatch()
		stalled := errors.Is(context.Cause(attemptCtx), errUploadStalled)
		cancelAttempt(nil)
		closeErr := file.Close() // Close file after request completes (success or fail)
		if err == nil && closeErr != nil {
			return ScottyFinalizeToken{}, fmt.Errorf("error closing file: %w", closeErr)
		}

		if err == nil {
			return result, nil
		}

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return ScottyFinalizeToken{}, ctx.Err()
		}
		if stalled {
			err = fmt.Errorf("%w: no progress for %s", errUploadStalled, uploadStallTimeout)
			retryable = true
		}
		lastErr = err
		if !retryable {
			return ScottyFinalizeToken{}, fmt.Errorf("upload failed after %d attempt(s): %w", attemptNum, err)
		}
	}

	return ScottyFinalizeToken{}, fmt.Errorf("upload failed after %d attempts: %w", retryConfig.MaxRetries+1, lastErr)
}

// doUploadRequest performs a single upload attempt. retryable reports whether
// a failed attempt is worth repeating: network errors, 5xx and 429 are; other
// rejections are not.
func (a *Api) doUploadRequest(ctx context.Context, uploadURL string, reader io.Reader) (token ScottyFinalizeToken, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, reader)
	if err != nil {
		return ScottyFinalizeToken{}, false, fmt.Errorf("error creating request: %w", err)
	}

	// Use chunked transfer encoding (don't set ContentLength)
	req.ContentLength = -1

	bearerToken, err := a.BearerToken()
	if err != nil {
		return ScottyFinalizeToken{}, true, fmt.Errorf("failed to get bearer token: %w", err)
	}

	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", a.language)
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return ScottyFinalizeToken{}, true, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ScottyFinalizeToken{}, ShouldRetry(resp, nil), newAPIError(resp)
	}

	bodyBytes, err := ReadResponseBody(resp)
	if err != nil {
		return ScottyFinalizeToken{}, true, fmt.Errorf("failed to read response body: %w", err)
	}
	token, err = ParseScottyFinalizeToken(bodyBytes)
	if err != nil {
		return ScottyFinalizeToken{}, false, fmt.Errorf("invalid upload finalize response: %w", err)
	}
	return token, false, nil
}

// CommitUpload commits the upload to Google Photos
func (a *Api) CommitUpload(
	uploadResponseDecoded *generated.CommitToken,
	fileName string,
	sha1Hash []byte,
	uploadTimestamp int64,
) (string, error) {
	if uploadTimestamp == 0 {
		uploadTimestamp = time.Now().Unix()
	}

	var qualityVal int64 = 3
	if a.saver {
		qualityVal = 1
		a.model = "Pixel 2"
	}

	if a.useQuota {
		a.model = "Pixel 8"
	}

	unknownInt := int64(46000000)

	// Create the protobuf message
	protoBody := generated.CommitUpload{
		Field1: &generated.CommitUploadField1Type{
			Field1: &generated.CommitUploadField1TypeField1Type{
				Field1: uploadResponseDecoded.Field1,
				Field2: uploadResponseDecoded.Field2,
			},
			FileName: fileName,
			Sha1Hash: sha1Hash,
			Field4: &generated.CommitUploadField1TypeField4Type{
				FileLastModifiedTimestamp: uploadTimestamp,
				Field2:                    unknownInt,
			},
			Quality: qualityVal,
			Field10: 1,
		},
		Field2: &generated.CommitUploadField2Type{
			Model:             a.model,
			Make:              a.make,
			AndroidApiVersion: a.androidAPIVersion,
		},
		Field3: []byte{1, 3},
	}

	// Serialize the protobuf message
	serializedData, err := proto.Marshal(&protoBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf: %w", err)
	}

	return a.commitSerialized(serializedData)
}

func (a *Api) CommitLivePhoto(input LivePhotoCreateRequest) (string, error) {
	serializedData, err := BuildLivePhotoCreateMediaItemsRequest(input)
	if err != nil {
		return "", fmt.Errorf("build Live Photo create request: %w", err)
	}
	return a.commitSerialized(serializedData)
}

func (a *Api) ReconcileLivePhoto(input LivePhotoReconcileRequest) (string, error) {
	serializedData, err := BuildLivePhotoReconcileMediaItemsRequest(input)
	if err != nil {
		return "", fmt.Errorf("build Live Photo reconcile request: %w", err)
	}
	return a.commitSerialized(serializedData)
}

func (a *Api) commitSerialized(serializedData []byte) (string, error) {
	retryConfig := DefaultRetryConfig()
	if a.commitRetryConfig != nil {
		retryConfig = *a.commitRetryConfig
	}
	var lastErr error
	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := CalculateBackoff(attempt-1, retryConfig)
			time.Sleep(delay)
		}
		mediaKey, retryable, err := a.doCommitRequest(serializedData)
		if err == nil {
			return mediaKey, nil
		}
		lastErr = err
		if !retryable {
			return "", fmt.Errorf("commit failed after %d attempt(s): %w", attempt+1, lastErr)
		}
	}
	return "", fmt.Errorf("commit failed after %d attempts: %w", retryConfig.MaxRetries+1, lastErr)
}

func (a *Api) doCommitRequest(serializedData []byte) (mediaKey string, retryable bool, err error) {
	headers := map[string]string{
		"accept-Encoding":          "gzip",
		"accept-Language":          a.language,
		"content-Type":             "application/x-protobuf",
		"user-Agent":               a.userAgent,
		"x-goog-ext-173412678-bin": "CgcIAhClARgC",
		"x-goog-ext-174067345-bin": "CgIIAg==",
	}

	endpoint := a.commitEndpoint
	if endpoint == "" {
		endpoint = photosCreateMediaItemsEndpoint
	}
	resp, err := a.send(func() (*http.Request, error) {
		return newPostRequest(endpoint, serializedData, headers)
	})
	if err != nil {
		// Network and token-refresh failures are worth retrying; a credential
		// Google refused is not.
		_, rejected := asAPIError(err)
		return "", !rejected, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", ShouldRetry(resp, nil), newAPIError(resp)
	}

	bodyBytes, err := ReadResponseBody(resp)
	if err != nil {
		return "", false, fmt.Errorf("failed to read accepted response body: %w", err)
	}

	// HTTP success may mean the media item already exists even when the minimal
	// response schema cannot validate it. Do not retry and risk a duplicate commit.
	mediaKey, err = parseCreateMediaItemsResponse(bodyBytes)
	if err != nil {
		return "", false, fmt.Errorf("failed to parse accepted response: %w", err)
	}
	return mediaKey, false, nil
}

// parseCreateMediaItemsResponse decodes only the verified media-key path. The
// generated minimal schema leaves every other private response field unknown,
// so proto.Unmarshal preserves forward compatibility without guessing types.
func parseCreateMediaItemsResponse(responseBytes []byte) (string, error) {
	var response generated.CreateMediaItemsResponse
	if err := proto.Unmarshal(responseBytes, &response); err != nil {
		return "", fmt.Errorf("unmarshal create-media response: %w", err)
	}
	for _, item := range response.GetItem() {
		if mediaKey := item.GetResultItem().GetMediaKey(); mediaKey != "" {
			return mediaKey, nil
		}
	}
	return "", fmt.Errorf("upload rejected by API: media key is empty or missing")
}

// CreateAlbum creates a new album with the given name and initial media items.
// Returns the album media key for subsequent AddMediaToAlbum calls.
func (a *Api) CreateAlbum(albumName string, mediaKeys []string) (string, error) {
	// Build media keys structure
	protoMediaKeys := make([]*generated.CreateAlbumField4Type, len(mediaKeys))
	for i, key := range mediaKeys {
		protoMediaKeys[i] = &generated.CreateAlbumField4Type{
			Field1: &generated.CreateAlbumField4TypeField1Type{
				MediaKey: key,
			},
		}
	}

	// Create the protobuf message
	protoBody := generated.CreateAlbum{
		AlbumName: albumName,
		Timestamp: time.Now().Unix(),
		Field3:    1,
		MediaKeys: protoMediaKeys,
		Field6:    &generated.CreateAlbumField6Type{},
		Field7:    &generated.CreateAlbumField7Type{Field1: 3},
		DeviceInfo: &generated.CreateAlbumField8Type{
			Model:             a.model,
			Make:              a.make,
			AndroidApiVersion: a.androidAPIVersion,
		},
	}

	// Serialize the protobuf message
	serializedData, err := proto.Marshal(&protoBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf: %w", err)
	}

	// Prepare headers
	headers := map[string]string{
		"Accept-Encoding":          "gzip",
		"Accept-Language":          a.language,
		"Content-Type":             "application/x-protobuf",
		"User-Agent":               a.userAgent,
		"x-goog-ext-173412678-bin": "CgcIAhClARgC",
		"x-goog-ext-174067345-bin": "CgIIAg==",
	}

	resp, err := a.send(func() (*http.Request, error) {
		return newPostRequest("https://photosdata-pa.googleapis.com/6439526531001121323/8386163679468898444", serializedData, headers)
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	// Check for errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newAPIError(resp)
	}

	// Parse the response body
	bodyBytes, err := ReadResponseBody(resp)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var pbResp generated.CreateAlbumResponse
	if err := proto.Unmarshal(bodyBytes, &pbResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}

	// Get album media key from response
	if pbResp.GetField1() == nil {
		return "", fmt.Errorf("create album failed: invalid response structure")
	}

	albumMediaKey := pbResp.GetField1().GetAlbumMediaKey()
	if albumMediaKey == "" {
		return "", fmt.Errorf("create album failed: no album media key returned")
	}

	return albumMediaKey, nil
}

// AddMediaToAlbum adds media items to an existing album.
func (a *Api) AddMediaToAlbum(albumMediaKey string, mediaKeys []string) error {
	// Create the protobuf message
	protoBody := generated.AddMediaToAlbum{
		MediaKeys:     mediaKeys,
		AlbumMediaKey: albumMediaKey,
		Field5:        &generated.AddMediaToAlbumField5Type{Field1: 2},
		DeviceInfo: &generated.AddMediaToAlbumField6Type{
			Model:             a.model,
			Make:              a.make,
			AndroidApiVersion: a.androidAPIVersion,
		},
		Timestamp: time.Now().Unix(),
	}

	// Serialize the protobuf message
	serializedData, err := proto.Marshal(&protoBody)
	if err != nil {
		return fmt.Errorf("failed to marshal protobuf: %w", err)
	}

	// Prepare headers
	headers := map[string]string{
		"Accept-Encoding":          "gzip",
		"Accept-Language":          a.language,
		"Content-Type":             "application/x-protobuf",
		"User-Agent":               a.userAgent,
		"x-goog-ext-173412678-bin": "CgcIAhClARgC",
		"x-goog-ext-174067345-bin": "CgIIAg==",
	}

	resp, err := a.send(func() (*http.Request, error) {
		return newPostRequest("https://photosdata-pa.googleapis.com/6439526531001121323/484917746253879292", serializedData, headers)
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Check for errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp)
	}

	return nil
}
