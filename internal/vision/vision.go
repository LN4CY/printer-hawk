// Package vision implements pure-Go JPEG scaling and pluggable dual-stage VLM failure checks
// across Ollama, Gemini, Anthropic Claude, and OpenAI-compatible vision endpoints.
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ln4cy/printer-hawk/internal/config"
)

var fenceRe = regexp.MustCompile("(?is)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// Telemetry carries printer context supplied to vision prompts.
type Telemetry struct {
	PrinterID    string
	PrinterName  string
	Model        string
	State        string
	Z            *float64
	BedTemp      *float64
	BedTarget    *float64
	ActiveTool   *int
	NozzleTemp   *float64
	NozzleTarget *float64
	Material     string
	JobProgress  *float64
	JobPath      string
}

// StageResult is the normalized output from Stage 1 or Stage 2 inspection.
type StageResult struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Status       string  `json:"status"`
	Confidence   float64 `json:"confidence"`
	Defect       string  `json:"defect"`
	Reason       string  `json:"reason"`
	ConfirmPause *bool   `json:"confirm_pause,omitempty"`
	Think        bool    `json:"think,omitempty"`
	LatencyMS    int64   `json:"latency_ms"`
	FallbackFrom string  `json:"fallback_from,omitempty"`
	PrimaryError string  `json:"primary_error,omitempty"`
}

// ResizeJPEG downscales oversized JPEGs using pure Go bilinear sampling to bound VLM tokens & latency.
func ResizeJPEG(raw []byte, maxWidth int, quality int) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty JPEG payload")
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode jpeg: %w", err)
	}
	b := img.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, errors.New("invalid image dimensions")
	}

	var dst image.Image = img
	if maxWidth > 0 && srcW > maxWidth {
		ratio := float64(maxWidth) / float64(srcW)
		dstH := max(1, int(math.Round(float64(srcH)*ratio)))
		dst = resizeBilinear(img, maxWidth, dstH)
	}

	if quality < 40 || quality > 95 {
		quality = 82
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return out.Bytes(), nil
}

func resizeBilinear(src image.Image, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	srcW := sb.Dx()
	srcH := sb.Dy()
	out := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	xScale := float64(srcW) / float64(dstW)
	yScale := float64(srcH) / float64(dstH)

	for y := range dstH {
		sy := (float64(y)+0.5)*yScale - 0.5
		y0 := clampInt(int(math.Floor(sy)), 0, srcH-1)
		y1 := clampInt(y0+1, 0, srcH-1)
		wy := math.Max(0, sy-float64(y0))
		for x := range dstW {
			sx := (float64(x)+0.5)*xScale - 0.5
			x0 := clampInt(int(math.Floor(sx)), 0, srcW-1)
			x1 := clampInt(x0+1, 0, srcW-1)
			wx := math.Max(0, sx-float64(x0))

			c00 := src.At(sb.Min.X+x0, sb.Min.Y+y0)
			c10 := src.At(sb.Min.X+x1, sb.Min.Y+y0)
			c01 := src.At(sb.Min.X+x0, sb.Min.Y+y1)
			c11 := src.At(sb.Min.X+x1, sb.Min.Y+y1)
			out.SetRGBA(x, y, bilerp(c00, c10, c01, c11, wx, wy))
		}
	}
	return out
}

func bilerp(c00, c10, c01, c11 color.Color, wx, wy float64) color.RGBA {
	r00, g00, b00, _ := c00.RGBA()
	r10, g10, b10, _ := c10.RGBA()
	r01, g01, b01, _ := c01.RGBA()
	r11, g11, b11, _ := c11.RGBA()
	mix := func(v00, v10, v01, v11 uint32) uint8 {
		top := float64(v00>>8)*(1-wx) + float64(v10>>8)*wx
		bot := float64(v01>>8)*(1-wx) + float64(v11>>8)*wx
		return uint8(clampInt(int(math.Round(top*(1-wy)+bot*wy)), 0, 255))
	}
	return color.RGBA{
		R: mix(r00, r10, r01, r11),
		G: mix(g00, g10, g01, g11),
		B: mix(b00, b10, b01, b11),
		A: 255,
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ParseJSONObject extracts a JSON object from raw text or a fenced markdown block.
func ParseJSONObject(raw string) (map[string]any, error) {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(cleaned), &obj); err == nil && obj != nil {
			return obj, nil
		}
	}
	if m := fenceRe.FindStringSubmatch(cleaned); len(m) == 2 {
		var obj map[string]any
		if err := json.Unmarshal([]byte(m[1]), &obj); err == nil && obj != nil {
			return obj, nil
		}
	}
	start := strings.IndexByte(cleaned, '{')
	end := strings.LastIndexByte(cleaned, '}')
	if start >= 0 && end > start {
		var obj map[string]any
		if err := json.Unmarshal([]byte(cleaned[start:end+1]), &obj); err == nil && obj != nil {
			return obj, nil
		}
	}
	return nil, errors.New("model output did not contain a valid JSON object")
}

// BuildContextPrompt builds concise printer telemetry + custom notes for vision prompts.
func BuildContextPrompt(tele Telemetry, triggerSource string, layerNum int, customNotes string) string {
	lines := []string{
		fmt.Sprintf("Printer: %s (model %s)", nonEmpty(tele.PrinterName, tele.PrinterID), nonEmpty(tele.Model, "unknown")),
		fmt.Sprintf("State: %s | Trigger: %s | Layer count: %d", nonEmpty(tele.State, "UNKNOWN"), triggerSource, layerNum),
	}
	if tele.Z != nil {
		lines = append(lines, fmt.Sprintf("Z height: %.2f mm", *tele.Z))
	} else {
		lines = append(lines, "Z height: unknown")
	}
	if tele.JobProgress != nil {
		lines = append(lines, fmt.Sprintf("Progress: %.1f%%", *tele.JobProgress))
	}
	if tele.JobPath != "" {
		lines = append(lines, "Job file: "+tele.JobPath)
	}
	if tele.BedTemp != nil {
		target := 0.0
		if tele.BedTarget != nil {
			target = *tele.BedTarget
		}
		lines = append(lines, fmt.Sprintf("Bed temp: %.1f/%.0f C", *tele.BedTemp, target))
	}
	if tele.ActiveTool != nil && tele.NozzleTemp != nil {
		target := 0.0
		if tele.NozzleTarget != nil {
			target = *tele.NozzleTarget
		}
		mat := nonEmpty(tele.Material, "unknown")
		lines = append(lines, fmt.Sprintf("Active tool T%d: %s, nozzle %.1f/%.0f C", *tele.ActiveTool, mat, *tele.NozzleTemp, target))
	}
	lines = append(lines,
		"",
		"Instructions:",
		"- Inspect the print bed and printed part in the latest image (and compare against earlier/previous layers if supplied).",
		"- Do NOT flag normal print features as failures: organic/tree/grid supports, prime/purge towers, dark or glossy filament reflections, shadows, or the print head/toolchanger moving in frame.",
		"- ONLY flag real actionable print failures: spaghetti / loose extruded strings, part detachment or severe warping off the bed, nozzle blobbing/engulfment, obvious layer shift, or clear air-printing.",
		"- If the printer is idle or the bed looks clean/normal, return status='ok', defect='none'.",
	)
	if strings.TrimSpace(customNotes) != "" {
		lines = append(lines, "- Operator notes: "+strings.TrimSpace(customNotes))
	}
	return strings.Join(lines, "\n")
}

// RunStage1 screens the current frame using Stage1Primary, automatically falling back to Stage1Fallback.
func RunStage1(ctx context.Context, primary, fallback config.ProviderSlot, currentJPG, prevJPG []byte, contextText string) (StageResult, error) {
	if !slotConfigured(primary) && !slotConfigured(fallback) {
		return StageResult{}, errors.New("no Stage 1 AI provider is configured")
	}
	if !slotConfigured(primary) {
		return callStage1Slot(ctx, fallback, currentJPG, prevJPG, contextText)
	}
	res, err := callStage1Slot(ctx, primary, currentJPG, prevJPG, contextText)
	if err == nil || !slotConfigured(fallback) {
		return res, err
	}
	fbRes, fbErr := callStage1Slot(ctx, fallback, currentJPG, prevJPG, contextText)
	if fbErr != nil {
		return StageResult{}, fmt.Errorf("primary (%s): %v; fallback (%s): %w", primary.Provider, err, fallback.Provider, fbErr)
	}
	fbRes.FallbackFrom = primary.Provider
	fbRes.PrimaryError = err.Error()
	return fbRes, nil
}

// RunStage2 confirms a Stage 1 escalation using Stage2Primary, falling back to Stage2Fallback.
func RunStage2(ctx context.Context, primary, fallback config.ProviderSlot, currentJPG, prevJPG, refJPG []byte, contextText string, s1 StageResult) (StageResult, error) {
	if !slotConfigured(primary) && !slotConfigured(fallback) {
		return StageResult{}, errors.New("no Stage 2 confirmation provider is configured")
	}
	if !slotConfigured(primary) {
		return callStage2Slot(ctx, fallback, currentJPG, prevJPG, refJPG, contextText, s1)
	}
	res, err := callStage2Slot(ctx, primary, currentJPG, prevJPG, refJPG, contextText, s1)
	if err == nil || !slotConfigured(fallback) {
		return res, err
	}
	fbRes, fbErr := callStage2Slot(ctx, fallback, currentJPG, prevJPG, refJPG, contextText, s1)
	if fbErr != nil {
		return StageResult{}, fmt.Errorf("primary (%s): %v; fallback (%s): %w", primary.Provider, err, fallback.Provider, fbErr)
	}
	fbRes.FallbackFrom = primary.Provider
	fbRes.PrimaryError = err.Error()
	return fbRes, nil
}

// TestProvider runs a lightweight text-only or connectivity check against one provider slot.
func TestProvider(ctx context.Context, slot config.ProviderSlot) (map[string]any, error) {
	if !slotConfigured(slot) {
		return nil, errors.New("provider slot is not configured (provider=none or missing model)")
	}
	// Keep provider connectivity checks safely inside Prusa Connect Local's 10s routes.serve budget.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := httpClientFor(slot)
	t0 := time.Now()

	switch slot.Provider {
	case config.ProviderOllama:
		base := nonEmpty(slot.BaseURL, "http://127.0.0.1:11434")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		_ = json.Unmarshal(body, &tags)
		found := false
		for _, m := range tags.Models {
			if m.Name == slot.Model {
				found = true
				break
			}
		}
		if !found && len(tags.Models) > 0 {
			return map[string]any{
				"ok":         false,
				"latency_ms": time.Since(t0).Milliseconds(),
				"detail":     fmt.Sprintf("reachable, but model %q was not listed in /api/tags (%d models installed)", slot.Model, len(tags.Models)),
			}, nil
		}
		return map[string]any{
			"ok":         true,
			"latency_ms": time.Since(t0).Milliseconds(),
			"detail":     fmt.Sprintf("Ollama reachable (%s ready)", slot.Model),
		}, nil

	case config.ProviderGemini, config.ProviderAnthropic, config.ProviderOpenAICompat:
		prompt := `Reply with exactly this JSON: {"status":"ok","confidence":1.0,"defect":"none","reason":"Connectivity test succeeded."}`
		res, err := invokeTextOrVision(ctx, slot, nil, prompt, stage1Schema())
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":         true,
			"latency_ms": res.LatencyMS,
			"detail":     fmt.Sprintf("%s (%s) responded cleanly: %s", slot.Provider, slot.Model, res.Reason),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider %q", slot.Provider)
	}
}

func slotConfigured(s config.ProviderSlot) bool {
	if s.Provider == "" || s.Provider == config.ProviderNone || s.Model == "" {
		return false
	}
	if (s.Provider == config.ProviderGemini || s.Provider == config.ProviderAnthropic) && s.APIKey == "" {
		return false
	}
	return true
}

func callStage1Slot(ctx context.Context, slot config.ProviderSlot, currentJPG, prevJPG []byte, contextText string) (StageResult, error) {
	frames := make([]labeledFrame, 0, 2)
	if len(prevJPG) > 0 {
		frames = append(frames, labeledFrame{label: "Previous clean layer snapshot:", jpeg: prevJPG})
	}
	frames = append(frames, labeledFrame{label: "Current snapshot to inspect:", jpeg: currentJPG})

	prompt := contextText + "\n\nReturn ONLY a JSON object matching: " +
		`{"status":"ok"|"warning"|"failure","confidence":0.0-1.0,"defect":"none"|"spaghetti"|"detachment"|"blob"|"layer_shift"|"air_print"|"other","reason":"concise one-sentence explanation"}`

	res, err := invokeTextOrVision(ctx, slot, frames, prompt, stage1Schema())
	if err != nil {
		return StageResult{}, err
	}
	if res.Status != "ok" && res.Status != "warning" && res.Status != "failure" {
		res.Status = "ok"
	}
	return res, nil
}

func callStage2Slot(ctx context.Context, slot config.ProviderSlot, currentJPG, prevJPG, refJPG []byte, contextText string, s1 StageResult) (StageResult, error) {
	frames := make([]labeledFrame, 0, 3)
	if len(refJPG) > 0 {
		frames = append(frames, labeledFrame{label: "Earlier baseline layer snapshot:", jpeg: refJPG})
	}
	if len(prevJPG) > 0 {
		frames = append(frames, labeledFrame{label: "Previous layer snapshot:", jpeg: prevJPG})
	}
	frames = append(frames, labeledFrame{label: "Current snapshot flagged by Stage 1:", jpeg: currentJPG})

	s1JSON, _ := json.Marshal(s1)
	prompt := fmt.Sprintf("%s\n\nStage 1 screener (%s/%s) flagged: %s\n\n"+
		"You are the final conservative safety gate before sending an automated PAUSE_PRINT command. "+
		"Confirm pause ONLY if there is clear visual evidence of an active print failure "+
		"(spaghetti, bed detachment, nozzle blob, layer shift, or air printing). "+
		"Rule out normal supports, purge towers, shadows, dark glossy plastic, or toolhead motion.\n\n"+
		`Respond ONLY with a raw JSON object: {"confirm_pause":boolean,"status":"ok"|"warning"|"failure","confidence":0.0-1.0,"defect":"none"|"spaghetti"|"detachment"|"blob"|"layer_shift"|"air_print"|"other","reason":"concise explanation"}`,
		contextText, s1.Provider, s1.Model, string(s1JSON))

	return invokeTextOrVision(ctx, slot, frames, prompt, stage2Schema())
}

type labeledFrame struct {
	label string
	jpeg  []byte
}

func invokeTextOrVision(ctx context.Context, slot config.ProviderSlot, frames []labeledFrame, prompt string, schema map[string]any) (StageResult, error) {
	client := httpClientFor(slot)
	t0 := time.Now()
	var text string
	var err error

	switch slot.Provider {
	case config.ProviderOllama:
		text, err = postOllama(ctx, client, slot, frames, prompt, schema)
	case config.ProviderGemini:
		text, err = postGemini(ctx, client, slot, frames, prompt, schema)
	case config.ProviderAnthropic:
		text, err = postAnthropic(ctx, client, slot, frames, prompt)
	case config.ProviderOpenAICompat:
		text, err = postOpenAICompat(ctx, client, slot, frames, prompt)
	default:
		return StageResult{}, fmt.Errorf("unsupported provider %q", slot.Provider)
	}
	latency := time.Since(t0).Milliseconds()
	if err != nil {
		return StageResult{}, err
	}

	parsed, err := ParseJSONObject(text)
	if err != nil {
		return StageResult{}, err
	}
	res := StageResult{
		Provider:   slot.Provider,
		Model:      slot.Model,
		Status:     strings.ToLower(strings.TrimSpace(asString(parsed["status"], "ok"))),
		Confidence: clampConfidence(parsed["confidence"]),
		Defect:     strings.ToLower(strings.TrimSpace(asString(parsed["defect"], "none"))),
		Reason:     strings.TrimSpace(asString(parsed["reason"], "")),
		Think:      slot.Think,
		LatencyMS:  latency,
	}
	if cp, ok := parsed["confirm_pause"]; ok {
		b := asBool(cp)
		res.ConfirmPause = &b
	}
	return res, nil
}

func httpClientFor(slot config.ProviderSlot) *http.Client {
	connDur := time.Duration(slot.ConnectTimeoutSec * float64(time.Second))
	if connDur <= 0 {
		connDur = 5 * time.Second
	}
	readDur := time.Duration(slot.ReadTimeoutSec * float64(time.Second))
	if readDur <= 0 {
		readDur = 60 * time.Second
	}
	dialer := &net.Dialer{Timeout: connDur}
	return &http.Client{
		Timeout: connDur + readDur,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: connDur,
			ResponseHeaderTimeout: readDur,
		},
	}
}

func postOllama(ctx context.Context, client *http.Client, slot config.ProviderSlot, frames []labeledFrame, prompt string, schema map[string]any) (string, error) {
	base := nonEmpty(slot.BaseURL, "http://127.0.0.1:11434")
	var imgs []string
	var frameNotes []string
	for idx, f := range frames {
		imgs = append(imgs, base64.StdEncoding.EncodeToString(f.jpeg))
		frameNotes = append(frameNotes, fmt.Sprintf("- Image %d: %s", idx+1, strings.TrimSuffix(f.label, ":")))
	}
	fullPrompt := prompt
	if len(frameNotes) > 0 {
		fullPrompt = "Attached camera images in order:\n" + strings.Join(frameNotes, "\n") + "\n\n" + prompt
	}
	numPredict := 400
	if slot.Think {
		numPredict = 2048
	}
	payload := map[string]any{
		"model": slot.Model,
		"messages": []map[string]any{
			{"role": "user", "content": fullPrompt, "images": imgs},
		},
		"stream": false,
		"think":  slot.Think,
		"format": schema,
		"options": map[string]any{
			"temperature": 0.1,
			"num_ctx":     16384,
			"num_predict": numPredict,
		},
	}
	raw, err := doJSONPost(ctx, client, base+"/api/chat", nil, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", errors.New("ollama returned empty message content")
	}
	return out.Message.Content, nil
}

func postGemini(ctx context.Context, client *http.Client, slot config.ProviderSlot, frames []labeledFrame, prompt string, schema map[string]any) (string, error) {
	base := nonEmpty(slot.BaseURL, "https://generativelanguage.googleapis.com")
	var parts []map[string]any
	for _, f := range frames {
		parts = append(parts,
			map[string]any{"text": f.label},
			map[string]any{"inline_data": map[string]any{
				"mime_type": "image/jpeg",
				"data":      base64.StdEncoding.EncodeToString(f.jpeg),
			}},
		)
	}
	parts = append(parts, map[string]any{"text": prompt})
	payload := map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"temperature":      0.1,
			"responseMimeType": "application/json",
			"responseSchema":   schema,
		},
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", base, slot.Model)
	headers := map[string]string{"x-goog-api-key": slot.APIKey}
	raw, err := doJSONPost(ctx, client, url, headers, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 {
		return "", errors.New("gemini returned no candidates")
	}
	var sb strings.Builder
	for _, p := range out.Candidates[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	return sb.String(), nil
}

func postAnthropic(ctx context.Context, client *http.Client, slot config.ProviderSlot, frames []labeledFrame, prompt string) (string, error) {
	base := nonEmpty(slot.BaseURL, "https://api.anthropic.com")
	var content []map[string]any
	for _, f := range frames {
		content = append(content,
			map[string]any{"type": "text", "text": f.label},
			map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": "image/jpeg",
					"data":       base64.StdEncoding.EncodeToString(f.jpeg),
				},
			},
		)
	}
	content = append(content, map[string]any{"type": "text", "text": prompt})
	payload := map[string]any{
		"model":      slot.Model,
		"max_tokens": 450,
		"messages":   []map[string]any{{"role": "user", "content": content}},
	}
	headers := map[string]string{
		"x-api-key":         slot.APIKey,
		"anthropic-version": "2023-06-01",
	}
	raw, err := doJSONPost(ctx, client, base+"/v1/messages", headers, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, b := range out.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String(), nil
}

func postOpenAICompat(ctx context.Context, client *http.Client, slot config.ProviderSlot, frames []labeledFrame, prompt string) (string, error) {
	base := nonEmpty(slot.BaseURL, "https://api.openai.com/v1")
	var content []map[string]any
	for _, f := range frames {
		content = append(content,
			map[string]any{"type": "text", "text": f.label},
			map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(f.jpeg),
				},
			},
		)
	}
	content = append(content, map[string]any{"type": "text", "text": prompt})
	payload := map[string]any{
		"model":       slot.Model,
		"temperature": 0.1,
		"max_tokens":  450,
		"messages":    []map[string]any{{"role": "user", "content": content}},
	}
	headers := map[string]string{}
	if slot.APIKey != "" {
		headers["Authorization"] = "Bearer " + slot.APIKey
	}
	endpoint := base + "/chat/completions"
	if strings.HasSuffix(base, "/chat/completions") {
		endpoint = base
	}
	raw, err := doJSONPost(ctx, client, endpoint, headers, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("openai-compatible API returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func doJSONPost(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(respBytes))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, msg)
	}
	return respBytes, nil
}

func stage1Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":     map[string]any{"type": "string", "enum": []string{"ok", "warning", "failure"}},
			"confidence": map[string]any{"type": "number"},
			"defect":     map[string]any{"type": "string", "enum": []string{"none", "spaghetti", "detachment", "blob", "layer_shift", "air_print", "other"}},
			"reason":     map[string]any{"type": "string"},
		},
		"required": []string{"status", "confidence", "defect", "reason"},
	}
}

func stage2Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"confirm_pause": map[string]any{"type": "boolean"},
			"status":        map[string]any{"type": "string", "enum": []string{"ok", "warning", "failure"}},
			"confidence":    map[string]any{"type": "number"},
			"defect":        map[string]any{"type": "string", "enum": []string{"none", "spaghetti", "detachment", "blob", "layer_shift", "air_print", "other"}},
			"reason":        map[string]any{"type": "string"},
		},
		"required": []string{"confirm_pause", "status", "confidence", "defect", "reason"},
	}
}

func clampConfidence(v any) float64 {
	num, ok := v.(float64)
	if !ok {
		return 0
	}
	return math.Max(0, math.Min(1, num))
}

func asString(v any, def string) string {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func asBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return strings.TrimSpace(s)
}
