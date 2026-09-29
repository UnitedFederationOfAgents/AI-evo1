package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/transcribestreaming"
	"github.com/aws/aws-sdk-go-v2/service/transcribestreaming/types"
	"github.com/aws/smithy-go/logging"
	"golang.org/x/net/http2"
)

// Audio format for the browser's captured microphone stream: 16-bit signed
// little-endian PCM, mono, 44.1kHz -- the same shape
// ignored-scratch/AI-sandboxing/agent-scribe (server.js/index.html) records
// and streams, so this replicates agent-scribe's technique (capture via
// ScriptProcessorNode, stream raw PCM chunks) with our own transport (this
// app's typed WebSocket protocol, see main.go's handleClientMsg) and our own
// AWS Transcribe client (aws-sdk-go-v2's transcribestreaming instead of
// @aws-sdk/client-transcribe-streaming).
const (
	transcribeSampleRateHz = 44100
	transcribeLanguage     = types.LanguageCodeEnUs
)

// TranscriptMsg is the "transcript" WebSocket payload pushed to the frontend
// as AWS Transcribe returns partial/final results, mirroring agent-scribe's
// 'transcription' socket.io event.
type TranscriptMsg struct {
	Text    string `json:"text"`
	IsFinal bool   `json:"is_final"`
}

// SaveResultMsg is the "save-result" WebSocket payload reporting whether the
// transcript accumulated so far was written into local-representative's
// files area, mirroring agent-scribe's 'saveResult'/'storeResult' events.
type SaveResultMsg struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	FileID  string `json:"file_id,omitempty"`
	Name    string `json:"name,omitempty"`
}

// transcribeSession holds one browser tab's in-progress AWS Transcribe
// streaming session: the live event stream plus the transcript accumulated
// from its final results so far, ready to be handed to saveTranscript.
type transcribeSession struct {
	cancel context.CancelFunc
	stream *transcribestreaming.StartStreamTranscriptionEventStream

	mu    sync.Mutex
	final strings.Builder
}

// startTranscription opens a new AWS Transcribe streaming session for c,
// mirroring agent-scribe's 'startTranscription' handler. A session already
// running for this client is left alone -- the frontend disables the Start
// button while recording, so this only guards against a stale/duplicate
// message.
func (s *Server) startTranscription(c *wsClient) {
	c.transcribeMu.Lock()
	if c.transcribe != nil {
		c.transcribeMu.Unlock()
		return
	}
	c.transcribeMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	var opts []func(*config.LoadOptions) error
	if s.awsRegion != "" {
		opts = append(opts, config.WithRegion(s.awsRegion))
	}
	// Revision A: the region/HTTP2 fixes from F/G/this substep's first Reply
	// didn't clear the "not found, Signing" error, so before guessing again
	// we need the SDK to tell us what it's actually doing. LogRetries +
	// LogSigning surface the resolved endpoint, the retry history, and the
	// canonical request/string-to-sign SigV4 builds (not the secret key
	// itself) -- exactly the layer this error's message is too terse to
	// diagnose on its own. Deliberately omitting LogRequest/ResponseWithBody:
	// StartStreamTranscription's initial request/response are logged fine
	// without it, and turning it on would also apply to every subsequent
	// in-stream audio frame once transcription is running.
	opts = append(opts,
		config.WithClientLogMode(aws.LogRetries|aws.LogSigning),
		config.WithLogger(logging.NewStandardLogger(os.Stderr)),
	)
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		cancel()
		log.Printf("transcribe: loading AWS config: %v", err)
		s.sendToClient(c, "error", fmt.Sprintf("loading AWS config: %v", err))
		return
	}
	// LoadDefaultConfig does not error when no region is found anywhere in
	// its chain (env/shared config/instance role) -- it just leaves
	// cfg.Region empty and defers the failure to the first call that needs
	// one, which then fails deep inside endpoint/signing resolution with a
	// cryptic SDK error. Credentials (AWS_ACCESS_KEY_ID/SECRET) being set
	// does not imply a region is too, so check explicitly and fail with an
	// actionable message instead.
	if cfg.Region == "" {
		cancel()
		const msg = "no AWS region configured: set AWS_REGION (or AWS_DEFAULT_REGION) in the environment, add a region to the shared AWS config, or start with --aws-region"
		log.Printf("transcribe: %s", msg)
		s.sendToClient(c, "error", "starting AWS Transcribe: "+msg)
		return
	}

	// StartStreamTranscription is a bidirectional event stream, which AWS
	// only serves over HTTP/2. Revision A: setting tr.ForceAttemptHTTP2 on
	// the buildable client's Transport (this substep's first Reply) turned
	// out not to be enough, and this is the likely reason why. Per
	// net/http.Transport's own onceSetNextProtoDefaults logic, Go only
	// auto-configures HTTP/2 when ForceAttemptHTTP2 *and* no custom
	// Dial/DialContext/DialTLS/DialTLSContext is already set on the
	// Transport -- ForceAttemptHTTP2 only overrides the (separate)
	// TLSClientConfig check, not that one. aws-sdk-go-v2's buildable HTTP
	// client always installs its own DialContext (to apply the SDK's
	// connection-timeout/keep-alive options), so on this Transport that
	// early-return guard fires regardless of ForceAttemptHTTP2, and HTTP/2
	// is silently never configured -- the request still falls back to
	// HTTP/1.1, and never gets far enough to reach a real credentials/region
	// check, surfacing as the same terse "not found, Signing" error. Calling
	// http2.ConfigureTransport(tr) directly (from golang.org/x/net/http2,
	// matching what AWS's own Go v2 transcribe-streaming example actually
	// does -- it does not use ForceAttemptHTTP2) rewrites tr's
	// TLSClientConfig/TLSNextProto itself instead of relying on that
	// opportunistic autodetection, so it takes effect even with a custom
	// dialer already in place.
	//
	// Compare agent-scribe (ignored-scratch/AI-sandboxing/agent-scribe,
	// server.js): its `new TranscribeStreamingClient({ region })` needs no
	// equivalent HTTP/2 configuration at all, because the JS SDK v3
	// middleware stack picks a Node http2-specific request handler
	// (NodeHttp2Handler) automatically for any operation modeled as an
	// event stream -- there is no opportunistic "maybe fall back to
	// HTTP/1.1" detection to fight with in the first place. aws-sdk-go-v2
	// has no equivalent auto-detection for its Go HTTP client, which is
	// exactly why this needs to be wired by hand here.
	var http2ConfigErr error
	buildableClient := awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		if err := http2.ConfigureTransport(tr); err != nil {
			http2ConfigErr = err
		}
	})
	if http2ConfigErr != nil {
		cancel()
		log.Printf("transcribe: configuring HTTP/2 transport: %v", http2ConfigErr)
		s.sendToClient(c, "error", fmt.Sprintf("starting AWS Transcribe: configuring HTTP/2 transport: %v", http2ConfigErr))
		return
	}
	client := transcribestreaming.NewFromConfig(cfg, func(o *transcribestreaming.Options) {
		o.HTTPClient = &protoLoggingHTTPClient{inner: buildableClient}
	})
	out, err := client.StartStreamTranscription(ctx, &transcribestreaming.StartStreamTranscriptionInput{
		LanguageCode:         transcribeLanguage,
		MediaEncoding:        types.MediaEncodingPcm,
		MediaSampleRateHertz: aws.Int32(transcribeSampleRateHz),
	})
	if err != nil {
		cancel()
		log.Printf("transcribe: starting AWS Transcribe (region %q): %v", cfg.Region, err)
		logErrorChain(err)
		s.sendToClient(c, "error", fmt.Sprintf("starting AWS Transcribe: %v", err))
		return
	}

	sess := &transcribeSession{cancel: cancel, stream: out.GetStream()}
	c.transcribeMu.Lock()
	c.transcribe = sess
	c.transcribeMu.Unlock()

	go s.readTranscriptEvents(c, sess)
	log.Printf("transcribe: session started")
}

// protoLoggingHTTPClient wraps an aws.HTTPClient and logs which HTTP
// protocol version each response actually came back on (resp.Proto, e.g.
// "HTTP/2.0" vs "HTTP/1.1"). Added in Revision A: the previous fix attempt
// (ForceAttemptHTTP2) *looked* like it should force HTTP/2 but apparently
// didn't take effect, so rather than guess again this makes the actual
// negotiated protocol directly observable in the logs going forward,
// instead of only inferable from whether the "not found, Signing" error
// recurs.
type protoLoggingHTTPClient struct {
	inner aws.HTTPClient
}

func (c *protoLoggingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(req)
	if err != nil {
		log.Printf("transcribe: %s %s: request failed before a response was received: %v", req.Method, req.URL, err)
		return resp, err
	}
	log.Printf("transcribe: %s %s -> %s %s", req.Method, req.URL, resp.Proto, resp.Status)
	return resp, err
}

// logErrorChain logs every layer of err's wrapped chain (via errors.Unwrap),
// each with its concrete Go type, so a terse top-level message like
// "not found, Signing" -- which is exactly as much as %v prints, no more --
// doesn't need to be guessed at: any additional context an inner error
// carries (that just doesn't make it into the outer error's Error() string)
// shows up on its own line. Added in Revision A alongside the HTTP/2 and
// SigV4 (ClientLogMode) logging above, for the same reason.
func logErrorChain(err error) {
	for i := 0; err != nil; i++ {
		log.Printf("transcribe: error chain [%d] (%T): %v", i, err, err)
		err = errors.Unwrap(err)
	}
}

// readTranscriptEvents drains sess's AWS Transcribe result stream, pushing
// each partial/final segment to c as a "transcript" message and accumulating
// final segments into sess.final for a later saveTranscript. Runs until the
// stream ends (stopTranscription cancelling its context, or AWS closing it),
// then closes it. Runs in its own goroutine.
func (s *Server) readTranscriptEvents(c *wsClient, sess *transcribeSession) {
	defer sess.stream.Close()
	for event := range sess.stream.Events() {
		te, ok := event.(*types.TranscriptResultStreamMemberTranscriptEvent)
		if !ok {
			continue
		}
		for _, res := range te.Value.Transcript.Results {
			if len(res.Alternatives) == 0 {
				continue
			}
			text := aws.ToString(res.Alternatives[0].Transcript)
			if strings.TrimSpace(text) == "" {
				continue
			}
			isFinal := !res.IsPartial
			if isFinal {
				sess.mu.Lock()
				if sess.final.Len() > 0 {
					sess.final.WriteString(" ")
				}
				sess.final.WriteString(text)
				sess.mu.Unlock()
			}
			s.sendToClient(c, "transcript", TranscriptMsg{Text: text, IsFinal: isFinal})
		}
	}
	if err := sess.stream.Err(); err != nil {
		log.Printf("transcribe: stream error: %v", err)
		logErrorChain(err)
		s.sendToClient(c, "error", fmt.Sprintf("AWS Transcribe stream error: %v", err))
	}
}

// sendAudioChunk forwards one base64-encoded PCM chunk from the browser
// (the "audio-chunk" WebSocket message) into c's live AWS Transcribe
// session. A no-op if no session is running (e.g. a chunk that raced
// stopTranscription).
func (s *Server) sendAudioChunk(c *wsClient, b64 string) {
	c.transcribeMu.Lock()
	sess := c.transcribe
	c.transcribeMu.Unlock()
	if sess == nil || b64 == "" {
		return
	}
	chunk, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("transcribe: decoding audio chunk: %v", err)
		return
	}
	if err := sess.stream.Send(context.Background(), &types.AudioStreamMemberAudioEvent{
		Value: types.AudioEvent{AudioChunk: chunk},
	}); err != nil {
		log.Printf("transcribe: sending audio chunk: %v", err)
	}
}

// stopTranscription ends c's AWS Transcribe session, if any, mirroring
// agent-scribe's 'stopTranscription' handler. Safe to call with no session
// running (e.g. from handleWS's disconnect cleanup).
func (s *Server) stopTranscription(c *wsClient) {
	c.transcribeMu.Lock()
	sess := c.transcribe
	c.transcribe = nil
	c.transcribeMu.Unlock()
	if sess == nil {
		return
	}
	sess.cancel()
}

// saveTranscript writes c's accumulated final transcript into
// local-representative's files area -- identically to how a browser upload
// would (see local-representative/files.go's handleFileUpload and
// local-representative/frontend/src/App.tsx's uploadFiles) -- rather than
// agent-scribe's HEURISTIC.md intake. It reuses the-conversationalist's
// existing representable link to local-representative to resolve LR's HTTP
// base URL, the same way condoccer/resources.go's fetchHighlightedFiles
// does.
func (s *Server) saveTranscript(c *wsClient) {
	c.transcribeMu.Lock()
	sess := c.transcribe
	c.transcribeMu.Unlock()

	var text string
	if sess != nil {
		sess.mu.Lock()
		text = strings.TrimSpace(sess.final.String())
		sess.mu.Unlock()
	}
	if text == "" {
		s.sendToClient(c, "save-result", SaveResultMsg{Success: false, Error: "no transcript to save"})
		return
	}

	s.reprMu.Lock()
	client := s.reprClient
	host := s.reprHost
	s.reprMu.Unlock()
	if client == nil {
		s.sendToClient(c, "save-result", SaveResultMsg{Success: false, Error: "not connected to local-representative"})
		return
	}
	lrHTTPPort := client.PeerHTTPPort()
	if lrHTTPPort == "" {
		s.sendToClient(c, "save-result", SaveResultMsg{Success: false, Error: "local-representative has not disclosed its HTTP port"})
		return
	}
	base := "http://" + net.JoinHostPort(host, lrHTTPPort)
	name := fmt.Sprintf("transcript-%s.txt", time.Now().Format("2006-01-02T15-04-05"))

	uploaded, err := uploadToFiles(base, name, []byte(text))
	if err != nil {
		s.sendToClient(c, "save-result", SaveResultMsg{Success: false, Error: err.Error()})
		return
	}

	// Clear the accumulated transcript on a successful save, mirroring
	// agent-scribe's saveTranscription/storeTranscription clearing
	// sessionTranscript.
	if sess != nil {
		sess.mu.Lock()
		sess.final.Reset()
		sess.mu.Unlock()
	}
	s.sendToClient(c, "save-result", SaveResultMsg{Success: true, FileID: uploaded.ID, Name: uploaded.Name})
}

// uploadedFile mirrors the subset of local-representative's FileInfo JSON
// (local-representative/files.go) this caller reads back.
type uploadedFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// uploadToFiles POSTs one file into local-representative's files area
// exactly as the browser's own upload widget does: multipart/form-data with
// a single "file" field (see local-representative/frontend/src/App.tsx's
// uploadFiles and local-representative/files.go's handleFileUpload). LR's
// POST /api/files is only gated against agent-coordinator's proxy relay
// (files.go's proxiedHeader check), so a direct caller like this one -- not
// carrying that header -- needs no auth, matching Revision D's note that
// authX is deliberately deferred to a later increment in favor of
// host-level controls.
func uploadToFiles(base, filename string, content []byte) (*uploadedFile, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, base+"/api/files", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading transcript: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("uploading transcript: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Files []uploadedFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("uploading transcript: %w", err)
	}
	if len(result.Files) == 0 {
		return nil, fmt.Errorf("uploading transcript: local-representative returned no file")
	}
	return &result.Files[0], nil
}
