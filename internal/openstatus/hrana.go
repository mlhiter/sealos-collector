package openstatus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("database url is required")
	}
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

func (c *Client) Execute(ctx context.Context, sql string) (*ResultSet, error) {
	body := pipelineRequest{
		Requests: []pipelineRequestItem{
			{Type: "execute", Stmt: &statement{SQL: sql}},
			{Type: "close"},
		},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v3/pipeline", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("libsql http %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var decoded pipelineResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, fmt.Errorf("decode libsql response: %w", err)
	}
	if len(decoded.Results) == 0 {
		return nil, fmt.Errorf("libsql response had no results")
	}
	first := decoded.Results[0]
	if first.Type != "ok" {
		if first.Error.Message != "" {
			return nil, fmt.Errorf("libsql error: %s", first.Error.Message)
		}
		return nil, fmt.Errorf("libsql returned result type %q", first.Type)
	}
	if first.Response.Result == nil {
		return &ResultSet{}, nil
	}
	return first.Response.Result, nil
}

type pipelineRequest struct {
	Baton    *string               `json:"baton"`
	Requests []pipelineRequestItem `json:"requests"`
}

type pipelineRequestItem struct {
	Type string     `json:"type"`
	Stmt *statement `json:"stmt,omitempty"`
}

type statement struct {
	SQL string `json:"sql"`
}

type pipelineResponse struct {
	Results []pipelineResult `json:"results"`
}

type pipelineResult struct {
	Type     string               `json:"type"`
	Error    pipelineError        `json:"error"`
	Response pipelineResponseItem `json:"response"`
}

type pipelineError struct {
	Message string `json:"message"`
}

type pipelineResponseItem struct {
	Type   string     `json:"type"`
	Result *ResultSet `json:"result"`
}

type ResultSet struct {
	Columns []Column `json:"cols"`
	Rows    [][]Cell `json:"rows"`
}

type Column struct {
	Name string `json:"name"`
}

type Cell struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (r *ResultSet) FirstInt64() (int64, bool, error) {
	if len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
		return 0, false, nil
	}
	value := r.Rows[0][0]
	if value.Type == "null" {
		return 0, false, nil
	}
	var parsed int64
	if _, err := fmt.Sscan(value.Value, &parsed); err != nil {
		return 0, false, fmt.Errorf("parse integer %q: %w", value.Value, err)
	}
	return parsed, true, nil
}

func (r *ResultSet) FirstString() (string, bool) {
	if len(r.Rows) == 0 || len(r.Rows[0]) == 0 {
		return "", false
	}
	value := r.Rows[0][0]
	if value.Type == "null" {
		return "", false
	}
	return value.Value, true
}
