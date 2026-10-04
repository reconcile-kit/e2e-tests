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
	widgetGroup = "e2e.reconcile-kit.dev"
	widgetKind  = "e2e-widget"
	// gadgetKind — второй тип со своим контроллером, remoteNoteKind — тип только с RemoteClient
	// (см. fixtures/e2e-operator/api).
	gadgetKind     = "e2e-gadget"
	remoteNoteKind = "e2e-remote-note"
	testNamespace  = "default"
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
	if body == nil {
		return c.DoRaw(method, path, nil)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	return c.DoRaw(method, path, raw)
}

// DoRaw — как Do, но тело уходит как есть (например, заведомо битый JSON).
func (c *SMClient) DoRaw(method, path string, body []byte) (int, []byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
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
	return c.kindResourcePath(widgetKind, name)
}

func (c *SMClient) createPath() string {
	return c.kindCreatePath(widgetKind)
}

// kindResourcePath — путь ресурса произвольного kind в группе и namespace тестов.
func (c *SMClient) kindResourcePath(kind, name string) string {
	return c.kindCreatePath(kind) + "/" + url.PathEscape(name)
}

func (c *SMClient) kindCreatePath(kind string) string {
	return fmt.Sprintf("/api/v1/groups/%s/namespaces/%s/kinds/%s/resources",
		url.PathEscape(widgetGroup),
		url.PathEscape(testNamespace),
		url.PathEscape(kind),
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
	Annotations       map[string]string `json:"annotations"`
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

// CreateOpts — полный набор полей POST (CreateResource покрывает только spec и labels).
type CreateOpts struct {
	Kind        string // по умолчанию widgetKind
	ShardID     string
	Name        string
	Spec        json.RawMessage
	Labels      map[string]string
	Annotations map[string]string
	Finalizers  []string
}

// Create создаёт ресурс с произвольными полями; ошибка, если ответ не 201.
func (c *SMClient) Create(o CreateOpts) (*ResourceDTO, error) {
	kind := o.Kind
	if kind == "" {
		kind = widgetKind
	}
	body := createBody{
		ShardID:     o.ShardID,
		Name:        o.Name,
		Spec:        o.Spec,
		Labels:      o.Labels,
		Annotations: o.Annotations,
		Finalizers:  o.Finalizers,
	}
	var out ResourceDTO
	if err := c.doJSON(http.MethodPost, c.kindCreatePath(kind), body, http.StatusCreated, &out); err != nil {
		return nil, fmt.Errorf("create %s/%s: %w", kind, o.Name, err)
	}
	return &out, nil
}

// GetKind — GET ресурса произвольного kind; (nil, 404, nil) если ресурса нет.
func (c *SMClient) GetKind(kind, name string) (*ResourceDTO, int, error) {
	code, b, err := c.Do(http.MethodGet, c.kindResourcePath(kind, name), nil)
	if err != nil || code == http.StatusNotFound {
		return nil, code, err
	}
	if code != http.StatusOK {
		return nil, code, fmt.Errorf("get %s/%s: %d: %s", kind, name, code, string(b))
	}
	var out ResourceDTO
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, code, err
	}
	return &out, code, nil
}

// UpdateBody — тело PUT .../resources/{name}. Version == nil — без проверки версии.
type UpdateBody struct {
	ShardID     string            `json:"shard_id"`
	Spec        json.RawMessage   `json:"spec,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Finalizers  []string          `json:"finalizers,omitempty"`
	Version     *int              `json:"version,omitempty"`
}

// UpdateResource — PUT виджета; возвращает код и ресурс при 200.
func (c *SMClient) UpdateResource(name string, body UpdateBody) (*ResourceDTO, int, error) {
	return c.putResource(c.resourcePath(name), body)
}

// UpdateStatusBody — тело PUT .../resources/{name}/status.
type UpdateStatusBody struct {
	ShardID        string            `json:"shard_id"`
	Status         json.RawMessage   `json:"status,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Annotations    map[string]string `json:"annotations,omitempty"`
	Finalizers     []string          `json:"finalizers,omitempty"`
	Version        int               `json:"version"`
	CurrentVersion int               `json:"current_version"`
}

// UpdateStatus — PUT статуса виджета; возвращает код и ресурс при 200.
func (c *SMClient) UpdateStatus(name string, body UpdateStatusBody) (*ResourceDTO, int, error) {
	return c.putResource(c.resourcePath(name)+"/status", body)
}

func (c *SMClient) putResource(path string, body any) (*ResourceDTO, int, error) {
	code, b, err := c.Do(http.MethodPut, path, body)
	if err != nil || code != http.StatusOK {
		return nil, code, err
	}
	var out ResourceDTO
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, code, err
	}
	return &out, code, nil
}

// List — GET /api/v1/resources с фильтрами; ошибка, если ответ не 200. Не заданные
// resource_group / kind / namespace подставляются для виджетов: с авторизацией фильтр list
// должен целиком покрываться правилом, а правило тестового клиента ограничено ими.
func (c *SMClient) List(filters map[string]string) ([]ResourceDTO, error) {
	q := url.Values{}
	q.Set("resource_group", widgetGroup)
	q.Set("kind", widgetKind)
	q.Set("namespace", testNamespace)
	for k, v := range filters {
		q.Set(k, v)
	}
	var out []ResourceDTO
	if err := c.doJSON(http.MethodGet, "/api/v1/resources?"+q.Encode(), nil, http.StatusOK, &out); err != nil {
		return nil, fmt.Errorf("list %v: %w", filters, err)
	}
	return out, nil
}

// Names — имена ресурсов из ответа List.
func Names(items []ResourceDTO) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func (c *SMClient) doJSON(method, path string, body any, wantCode int, out any) error {
	code, b, err := c.Do(method, path, body)
	if err != nil {
		return err
	}
	if code != wantCode {
		return fmt.Errorf("%s %s: %d: %s", method, path, code, string(b))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}
