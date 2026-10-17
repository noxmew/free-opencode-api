package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/noxmew/free-opencode-api/internal/config"
	"github.com/noxmew/free-opencode-api/internal/protocol"
	"github.com/noxmew/free-opencode-api/internal/provider"
)

type Server struct {
	cfg      config.Config
	upstream *provider.Client
	logger   *slog.Logger
	mux      *http.ServeMux
}

type chatRequest struct {
	Model    string
	Messages []json.RawMessage
	Stream   bool
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

func New(cfg config.Config, upstream *provider.Client, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		upstream: upstream,
		logger:   logger,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /v1/models", s.models)
	s.mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
	s.mux.HandleFunc("POST /v1/responses", s.responses)
	return s
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := providerRequestID(r)
		w.Header().Set("X-Request-ID", requestID)
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	response, err := s.upstream.DoModels(ctx, provider.HeaderInput{Inbound: r.Header})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeAPIError(w, http.StatusGatewayTimeout, "upstream request timed out", "timeout_error", "upstream_timeout", "")
			return
		}
		s.logger.Error("upstream model request failed", "error", err)
		writeAPIError(w, http.StatusBadGateway, "upstream request failed", "upstream_error", "upstream_request_failed", "")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		s.writeUpstreamResponse(w, response, false)
		return
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.logger.Error("upstream model response read failed", "error", err)
		writeAPIError(w, http.StatusBadGateway, "invalid upstream model response", "upstream_error", "invalid_upstream_response", "")
		return
	}

	filteredBody, filtered := filterFreeModels(body, s.cfg.StripFreeModelSuffix)
	copyResponseHeaders(w, response.Header)
	if filtered {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(filteredBody)
}

const freeModelSuffix = "-free"

func filterFreeModels(body []byte, stripSuffix bool) ([]byte, bool) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return body, false
	}

	data, ok := payload["data"]
	trimmedData := bytes.TrimSpace(data)
	if !ok || len(trimmedData) == 0 || trimmedData[0] != '[' {
		return body, false
	}

	var models []json.RawMessage
	if err := json.Unmarshal(data, &models); err != nil {
		return body, false
	}
	freeModels := make([]json.RawMessage, 0, len(models))
	for _, modelJSON := range models {
		var model map[string]json.RawMessage
		if err := json.Unmarshal(modelJSON, &model); err != nil || model == nil {
			continue
		}
		var id string
		if err := json.Unmarshal(model["id"], &id); err != nil || !strings.HasSuffix(id, freeModelSuffix) {
			continue
		}
		if stripSuffix {
			trimmedID, err := json.Marshal(strings.TrimSuffix(id, freeModelSuffix))
			if err != nil {
				return body, false
			}
			model["id"] = trimmedID
			modelJSON, err = json.Marshal(model)
			if err != nil {
				return body, false
			}
		}
		freeModels = append(freeModels, modelJSON)
	}

	filteredData, err := json.Marshal(freeModels)
	if err != nil {
		return body, false
	}
	payload["data"] = filteredData
	filteredBody, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return filteredBody, true
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "request body is unreadable", "invalid_request_error", "invalid_body", "")
		return
	}
	// Keep body as the bytes received from the client. The parser only reads
	// fields needed by the gateway; the provider receives all client options
	// and only applies its upstream-specific model/compatibility adjustments.
	parsed, err := parseChatRequest(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request", "")
		return
	}
	requestID := w.Header().Get("X-Request-ID")
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	response, err := s.upstream.DoChat(ctx, body, provider.HeaderInput{
		Inbound: r.Header,
		Stream:  parsed.Stream,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeAPIError(w, http.StatusGatewayTimeout, "upstream request timed out", "timeout_error", "upstream_timeout", "")
			return
		}
		s.logger.Error("upstream request failed", "request_id", requestID, "error", err)
		writeAPIError(w, http.StatusBadGateway, "upstream request failed", "upstream_error", "upstream_request_failed", "")
		return
	}
	defer response.Body.Close()

	s.writeUpstreamResponse(w, response, parsed.Stream)
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "request body is unreadable", "invalid_request_error", "invalid_body", "")
		return
	}
	meta, err := protocol.ResponsesRequestMeta(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request", "")
		return
	}
	target := s.upstream.ProtocolForModel(r.Context(), meta.Model)
	upstreamBody := body
	switch target {
	case protocol.Chat:
		upstreamBody, err = protocol.ResponsesToChat(body)
	case protocol.Anthropic:
		upstreamBody, err = protocol.ResponsesToAnthropic(body)
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request", "")
		return
	}

	requestID := w.Header().Get("X-Request-ID")
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	response, err := s.upstream.DoProtocol(ctx, target, upstreamBody, provider.HeaderInput{
		Inbound: r.Header,
		Stream:  meta.Stream,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeAPIError(w, http.StatusGatewayTimeout, "upstream request timed out", "timeout_error", "upstream_timeout", "")
			return
		}
		s.logger.Error("upstream responses request failed", "request_id", requestID, "protocol", target.String(), "error", err)
		writeAPIError(w, http.StatusBadGateway, "upstream request failed", "upstream_error", "upstream_request_failed", "")
		return
	}
	defer response.Body.Close()

	toolTypes := protocol.ResponsesToolTypes(body)
	switch target {
	case protocol.Chat:
		s.writeChatAsResponses(w, response, meta.Stream, meta.Model, toolTypes)
	case protocol.Anthropic:
		s.writeAnthropicAsResponses(w, response, meta.Stream, meta.Model, toolTypes)
	default:
		s.writeResponsesResponse(w, response, meta.Stream)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.ServiceAPIKey == "" {
		return true
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !secureEqual(parts[1], s.cfg.ServiceAPIKey) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="free-opencode-api"`)
		writeAPIError(w, http.StatusUnauthorized, "authentication required", "authentication_error", "invalid_api_key", "")
		return false
	}
	return true
}

func (s *Server) writeUpstreamResponse(w http.ResponseWriter, response *http.Response, stream bool) {
	if !stream && response.StatusCode == http.StatusOK && isEventStream(response.Header.Get("Content-Type")) {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream stream", "upstream_error", "invalid_upstream_stream", "")
			return
		}
		converted, err := aggregateChatCompletionStream(body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream stream", "upstream_error", "invalid_upstream_stream", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(converted)
		return
	}

	copyResponseHeaders(w, response.Header)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(response.StatusCode)

	if stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		buffer := make([]byte, 32*1024)
		for {
			count, err := response.Body.Read(buffer)
			if count > 0 {
				if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
					return
				}
				flusher.Flush()
			}
			if err != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, response.Body)
}

func (s *Server) writePassthroughResponse(w http.ResponseWriter, response *http.Response, stream bool) {
	copyResponseHeaders(w, response.Header)
	if stream {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(response.StatusCode)

	if !stream {
		_, _ = io.Copy(w, response.Body)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	buffer := make([]byte, 32*1024)
	for {
		count, err := response.Body.Read(buffer)
		if count > 0 {
			if _, writeErr := w.Write(buffer[:count]); writeErr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) writeResponsesResponse(w http.ResponseWriter, response *http.Response, stream bool) {
	if !stream && response.StatusCode == http.StatusOK && isEventStream(response.Header.Get("Content-Type")) {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream response stream", "upstream_error", "invalid_upstream_stream", "")
			return
		}
		converted, err := aggregateResponsesStream(body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream response stream", "upstream_error", "invalid_upstream_stream", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Del("Content-Length")
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(converted)
		return
	}

	s.writePassthroughResponse(w, response, stream)
}

func (s *Server) writeChatAsResponses(w http.ResponseWriter, response *http.Response, stream bool, model string, toolTypes map[string]string) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.writeConvertedUpstreamError(w, response)
		return
	}
	if !stream {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream response", "upstream_error", "invalid_upstream_response", "")
			return
		}
		completion, err := chatCompletionJSON(body, response.Header.Get("Content-Type"))
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error(), "upstream_error", "invalid_upstream_response", "")
			return
		}
		converted, err := protocol.ChatCompletionToResponses(completion, protocol.NewResponseID(), toolTypes)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error(), "upstream_error", "invalid_upstream_response", "")
			return
		}
		writeJSON(w, http.StatusOK, converted)
		return
	}

	streamWriter := protocol.NewResponsesStream(w, responseFlusher(w), protocol.NewResponseID(), model)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := streamWriter.Begin(); err != nil {
		return
	}
	buffered := bufio.NewReaderSize(response.Body, 64*1024)
	prefix, _ := buffered.Peek(5)
	if isEventStream(response.Header.Get("Content-Type")) || bytes.HasPrefix(bytes.TrimSpace(prefix), []byte("data:")) {
		if err := protocol.StreamChatToResponses(buffered, streamWriter, model, toolTypes); err != nil {
			_ = streamWriter.Fail(err.Error())
		}
		return
	}
	body, err := io.ReadAll(buffered)
	if err != nil {
		_ = streamWriter.Fail("invalid upstream response")
		return
	}
	completion, err := chatCompletionJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		_ = streamWriter.Fail(err.Error())
		return
	}
	converted, err := protocol.ChatCompletionToResponses(completion, streamWriter.ID(), toolTypes)
	if err != nil {
		_ = streamWriter.Fail(err.Error())
		return
	}
	_ = streamWriter.Complete(converted)
}

func (s *Server) writeAnthropicAsResponses(w http.ResponseWriter, response *http.Response, stream bool, model string, toolTypes map[string]string) {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.writeConvertedUpstreamError(w, response)
		return
	}
	if !stream {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "invalid upstream response", "upstream_error", "invalid_upstream_response", "")
			return
		}
		message, err := anthropicMessageJSON(body, response.Header.Get("Content-Type"))
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error(), "upstream_error", "invalid_upstream_response", "")
			return
		}
		converted, err := protocol.AnthropicMessageToResponses(message, protocol.NewResponseID(), toolTypes)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error(), "upstream_error", "invalid_upstream_response", "")
			return
		}
		writeJSON(w, http.StatusOK, converted)
		return
	}

	streamWriter := protocol.NewResponsesStream(w, responseFlusher(w), protocol.NewResponseID(), model)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := streamWriter.Begin(); err != nil {
		return
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		_ = streamWriter.Fail("invalid upstream response")
		return
	}
	message, err := anthropicMessageJSON(body, response.Header.Get("Content-Type"))
	if err != nil {
		_ = streamWriter.Fail(err.Error())
		return
	}
	converted, err := protocol.AnthropicMessageToResponses(message, streamWriter.ID(), toolTypes)
	if err != nil {
		_ = streamWriter.Fail(err.Error())
		return
	}
	_ = streamWriter.Complete(converted)
}

func (s *Server) writeConvertedUpstreamError(w http.ResponseWriter, response *http.Response) {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	message := strings.TrimSpace(string(body))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if strings.TrimSpace(envelope.Error.Message) != "" {
			message = strings.TrimSpace(envelope.Error.Message)
		} else if strings.TrimSpace(envelope.Message) != "" {
			message = strings.TrimSpace(envelope.Message)
		}
	}
	if message == "" {
		message = "upstream request failed"
	}
	status := response.StatusCode
	if status < 400 {
		status = http.StatusBadGateway
	}
	writeAPIError(w, status, message, "upstream_error", "upstream_request_failed", "")
}

func chatCompletionJSON(body []byte, contentType string) ([]byte, error) {
	if isEventStream(contentType) || looksLikeSSE(body) {
		return aggregateChatCompletionStream(body)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("invalid upstream chat response")
	}
	return body, nil
}

func anthropicMessageJSON(body []byte, contentType string) ([]byte, error) {
	if isEventStream(contentType) || looksLikeSSE(body) {
		return protocol.AggregateAnthropicStream(body)
	}
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("invalid upstream anthropic response")
	}
	return body, nil
}

func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return bytes.HasPrefix(trimmed, []byte("data:")) || bytes.Contains(trimmed, []byte("\ndata:")) || bytes.Contains(trimmed, []byte("\r\ndata:"))
}

func responseFlusher(w http.ResponseWriter) func() {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return func() {}
	}
	return flusher.Flush
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

type completionChoice struct {
	role         string
	content      strings.Builder
	reasoning    strings.Builder
	finishReason *string
	toolCalls    map[int]*completionToolCall
}

type completionToolCall struct {
	id        string
	callType  string
	name      strings.Builder
	arguments strings.Builder
}

type completionChunk struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string  `json:"role"`
			Content          *string `json:"content"`
			Reasoning        *string `json:"reasoning"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

func aggregateChatCompletionStream(body []byte) ([]byte, error) {
	choices := make(map[int]*completionChoice)
	var id string
	var model string
	var created int64
	var usage json.RawMessage
	var data []string

	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		value := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		if value == "[DONE]" {
			return nil
		}
		var chunk completionChunk
		if err := json.Unmarshal([]byte(value), &chunk); err != nil {
			return err
		}
		if id == "" {
			id = chunk.ID
		}
		if model == "" {
			model = chunk.Model
		}
		if created == 0 {
			created = chunk.Created
		}
		if len(chunk.Usage) > 0 && !bytes.Equal(bytes.TrimSpace(chunk.Usage), []byte("null")) {
			usage = append(json.RawMessage(nil), chunk.Usage...)
		}
		for _, item := range chunk.Choices {
			choice := choices[item.Index]
			if choice == nil {
				choice = &completionChoice{toolCalls: make(map[int]*completionToolCall)}
				choices[item.Index] = choice
			}
			if choice.toolCalls == nil {
				choice.toolCalls = make(map[int]*completionToolCall)
			}
			if choice.role == "" {
				choice.role = item.Delta.Role
			}
			if item.Delta.Content != nil {
				choice.content.WriteString(*item.Delta.Content)
			}
			if item.Delta.Reasoning != nil {
				choice.reasoning.WriteString(*item.Delta.Reasoning)
			}
			if item.Delta.ReasoningContent != nil {
				choice.reasoning.WriteString(*item.Delta.ReasoningContent)
			}
			for _, toolCall := range item.Delta.ToolCalls {
				index := 0
				if toolCall.Index != nil {
					index = *toolCall.Index
				}
				state := choice.toolCalls[index]
				if state == nil {
					state = &completionToolCall{}
					choice.toolCalls[index] = state
				}
				if toolCall.ID != "" {
					state.id = toolCall.ID
				}
				if toolCall.Type != "" {
					state.callType = toolCall.Type
				}
				state.name.WriteString(toolCall.Function.Name)
				state.arguments.WriteString(toolCall.Function.Arguments)
			}
			if item.FinishReason != nil {
				finishReason := *item.FinishReason
				choice.finishReason = &finishReason
			}
		}
		return nil
	}

	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if err := consume(); err != nil {
				return nil, err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, strings.TrimSpace(string(line[len("data:"):])))
		}
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if len(choices) == 0 {
		return nil, fmt.Errorf("upstream stream contained no choices")
	}

	indices := make([]int, 0, len(choices))
	for index := range choices {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	outputChoices := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		choice := choices[index]
		role := choice.role
		if role == "" {
			role = "assistant"
		}
		message := map[string]any{
			"role":    role,
			"content": choice.content.String(),
		}
		if reasoning := choice.reasoning.String(); reasoning != "" {
			message["reasoning_content"] = reasoning
		}
		if len(choice.toolCalls) > 0 {
			indices := make([]int, 0, len(choice.toolCalls))
			for toolIndex := range choice.toolCalls {
				indices = append(indices, toolIndex)
			}
			sort.Ints(indices)
			toolCalls := make([]any, 0, len(indices))
			for _, toolIndex := range indices {
				state := choice.toolCalls[toolIndex]
				callType := state.callType
				if callType == "" {
					callType = "function"
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   state.id,
					"type": callType,
					"function": map[string]any{
						"name":      state.name.String(),
						"arguments": state.arguments.String(),
					},
				})
			}
			message["tool_calls"] = toolCalls
			if choice.content.Len() == 0 {
				message["content"] = nil
			}
		}
		var finishReason any
		if choice.finishReason != nil {
			finishReason = *choice.finishReason
		}
		outputChoices = append(outputChoices, map[string]any{
			"index":         index,
			"message":       message,
			"finish_reason": finishReason,
		})
	}

	result := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": outputChoices,
	}
	if len(usage) > 0 {
		result["usage"] = usage
	}
	return json.Marshal(result)
}

func aggregateResponsesStream(body []byte) ([]byte, error) {
	var data []string
	var completed json.RawMessage

	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		value := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		if value == "[DONE]" {
			return nil
		}

		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(value), &event); err != nil {
			return err
		}
		if (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed") && len(event.Response) > 0 {
			completed = append(json.RawMessage(nil), event.Response...)
		}
		return nil
	}

	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if err := consume(); err != nil {
				return nil, err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data = append(data, strings.TrimSpace(string(line[len("data:"):])))
		}
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return nil, fmt.Errorf("upstream response stream contained no completed response")
	}
	return completed, nil
}

func parseChatRequest(body []byte) (chatRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return chatRequest{}, fmt.Errorf("request body must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return chatRequest{}, fmt.Errorf("request body must contain one JSON value")
	}

	var result chatRequest
	modelRaw, ok := raw["model"]
	if !ok || json.Unmarshal(modelRaw, &result.Model) != nil || strings.TrimSpace(result.Model) == "" {
		return chatRequest{}, fmt.Errorf("model is required")
	}
	messagesRaw, ok := raw["messages"]
	if !ok || len(bytes.TrimSpace(messagesRaw)) == 0 || bytes.Equal(bytes.TrimSpace(messagesRaw), []byte("null")) {
		return chatRequest{}, fmt.Errorf("messages is required")
	}
	if err := json.Unmarshal(messagesRaw, &result.Messages); err != nil || len(result.Messages) == 0 {
		return chatRequest{}, fmt.Errorf("messages must be a non-empty array")
	}
	for _, messageRaw := range result.Messages {
		if err := validateMessage(messageRaw); err != nil {
			return chatRequest{}, err
		}
	}
	if streamRaw, ok := raw["stream"]; ok {
		if err := json.Unmarshal(streamRaw, &result.Stream); err != nil {
			return chatRequest{}, fmt.Errorf("stream must be a boolean")
		}
	}
	return result, nil
}

func validateMessage(raw json.RawMessage) error {
	var message chatMessage
	if err := json.Unmarshal(raw, &message); err != nil || strings.TrimSpace(message.Role) == "" {
		return fmt.Errorf("each message must contain a role")
	}
	content := bytes.TrimSpace(message.Content)
	if len(content) == 0 {
		return fmt.Errorf("each message must contain content")
	}
	if content[0] == '"' {
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return fmt.Errorf("message content must be text")
		}
		return nil
	}
	if content[0] != '[' {
		return fmt.Errorf("message content must be text")
	}
	var parts []chatContentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return fmt.Errorf("message content must be text")
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
		case "image_url":
			if message.Role == "tool" {
				return fmt.Errorf("tool message content only supports text")
			}
			if !validImageURL(part.ImageURL) {
				return fmt.Errorf("image_url parts must contain a URL")
			}
		default:
			return fmt.Errorf("message content only supports text and image_url parts")
		}
	}
	return nil
}

func validImageURL(raw json.RawMessage) bool {
	var object struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &object) == nil && strings.TrimSpace(object.URL) != "" {
		return true
	}
	var url string
	return json.Unmarshal(raw, &url) == nil && strings.TrimSpace(url) != ""
}

func copyResponseHeaders(w http.ResponseWriter, headers http.Header) {
	for _, name := range []string{"Content-Type", "Content-Encoding", "Cache-Control", "Retry-After"} {
		for _, value := range headers.Values(name) {
			w.Header().Add(name, value)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message, errorType, code, param string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{
		Message: message,
		Type:    errorType,
		Code:    code,
		Param:   param,
	}})
}

func secureEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func providerRequestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value
	}
	return randomRequestID()
}

func randomRequestID() string {
	return provider.NewRequestID()
}
