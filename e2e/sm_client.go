package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	widgetGroup   = "e2e.reconcile-kit.dev"
	widgetKind    = "e2e-widget"
	testNamespace = "default"
)

type SMClient struct {
	base   *url.URL
	client *http.Client
	token  string
}

func NewSMClient(baseURL string) (*SMClient, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	return &SMClient{
		base: u,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// WithToken возвращает копию клиента, которая отправляет Authorization: Bearer <token>.
func (c *SMClient) WithToken(token string) *SMClient {
	cp := *c
	cp.token = token
	return &cp
}

// Do выполняет произвольный запрос к state-manager и возвращает код и тело ответа.
func (c *SMClient) Do(method, path string, body any) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base.String()+path, r)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return res.StatusCode, b, err
}

func (c *SMClient) do(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.client.Do(req)
}

func (c *SMClient) resourcePath(name string) string {
	return fmt.Sprintf("/api/v1/groups/%s/namespaces/%s/kinds/%s/resources/%s",
		url.PathEscape(widgetGroup),
		url.PathEscape(testNamespace),
		url.PathEscape(widgetKind),
		url.PathEscape(name),
	)
}

func (c *SMClient) createPath() string {
	return fmt.Sprintf("/api/v1/groups/%s/namespaces/%s/kinds/%s/resources",
		url.PathEscape(widgetGroup),
		url.PathEscape(testNamespace),
		url.PathEscape(widgetKind),
	)
}

type createBody struct {
	ShardID     string            `json:"shard_id"`
	Name        string            `json:"name"`
	Spec        json.RawMessage   `json:"spec,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Finalizers  []string          `json:"finalizers,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type ResourceDTO struct {
	ResourceGroup     string            `json:"resource_group"`
	Kind              string            `json:"kind"`
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	ShardID           string            `json:"shard_id"`
	Version           int               `json:"version"`
	CurrentVersion    int               `json:"current_version"`
	Spec              json.RawMessage   `json:"spec"`
	Status            json.RawMessage   `json:"status"`
	Labels            map[string]string `json:"labels"`
	Finalizers        []string          `json:"finalizers"`
	DeletionTimestamp *time.Time        `json:"deletion_timestamp"`
}

func (c *SMClient) CreateResource(shardID, name string, spec json.RawMessage, labels map[string]string) (*ResourceDTO, error) {
	body := createBody{
		ShardID: shardID,
		Name:    name,
		Spec:    spec,
		Labels:  labels,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, c.base.String()+c.createPath(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("create %s: %s: %s", name, res.Status, string(b))
	}
	var out ResourceDTO
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *SMClient) GetResource(name string) (*ResourceDTO, int, error) {
	req, err := http.NewRequest(http.MethodGet, c.base.String()+c.resourcePath(name), nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := c.do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusNotFound {
		return nil, res.StatusCode, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("get %s: %s: %s", name, res.Status, string(b))
	}
	var out ResourceDTO
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, res.StatusCode, err
	}
	return &out, res.StatusCode, nil
}

func (c *SMClient) DeleteResource(name string) error {
	req, err := http.NewRequest(http.MethodDelete, c.base.String()+c.resourcePath(name), nil)
	if err != nil {
		return err
	}
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("delete %s: %s: %s", name, res.Status, string(b))
	}
	return nil
}

// ListResources GET /api/v1/resources с фильтрами (в т.ч. label_selector в формате api).
func (c *SMClient) ListResources(shardID, labelSelector string) ([]ResourceDTO, error) {
	q := url.Values{}
	q.Set("resource_group", widgetGroup)
	q.Set("kind", widgetKind)
	q.Set("namespace", testNamespace)
	q.Set("shard_id", shardID)
	q.Set("limit", "500")
	if labelSelector != "" {
		q.Set("label_selector", labelSelector)
	}
	u := c.base.String() + "/api/v1/resources?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list resources: %s: %s", res.Status, string(b))
	}
	var out []ResourceDTO
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type statusShape struct {
	Conditions []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	} `json:"conditions"`
	ReconcilePasses    int   `json:"reconcilePasses"`
	ObservedGeneration int64 `json:"observedGeneration"`
}

func readyTrueFromStatus(statusJSON json.RawMessage) (bool, error) {
	if len(statusJSON) == 0 {
		return false, nil
	}
	var st statusShape
	if err := json.Unmarshal(statusJSON, &st); err != nil {
		return false, err
	}
	for _, c := range st.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true, nil
		}
	}
	return false, nil
}

func waitUntil(timeout, step time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		ok, err := fn()
		if err != nil {
			last = err
		} else if ok {
			return nil
		}
		time.Sleep(step)
	}
	if last != nil {
		return fmt.Errorf("timeout: %w", last)
	}
	return fmt.Errorf("timeout")
}
