package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// HTTP-контракт state-manager без оператора: коды ответов, версии, лейблы, удаление, list.
// Каждый тест работает в своём шарде, поэтому тесты параллельны.

func intPtr(v int) *int { return &v }

// TestContract_CreateReturnsResource: 201 и все поля запроса в ответе и в GET; version=1, current_version=0.
func TestContract_CreateReturnsResource(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-create")
	created, err := cl.Create(CreateOpts{
		ShardID:     shard,
		Name:        name,
		Spec:        mustJSON(map[string]any{"marker": "m"}),
		Labels:      map[string]string{"team": "a"},
		Annotations: map[string]string{"note": "x"},
		Finalizers:  []string{"e2e.test/hold"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, created.Version)
	require.Equal(t, 0, created.CurrentVersion)
	require.Equal(t, shard, created.ShardID)

	r, _ := getWidget(t, cl, name)
	require.Equal(t, widgetGroup, r.ResourceGroup)
	require.Equal(t, widgetKind, r.Kind)
	require.Equal(t, testNamespace, r.Namespace)
	require.Equal(t, map[string]string{"team": "a"}, r.Labels)
	require.Equal(t, map[string]string{"note": "x"}, r.Annotations)
	require.Equal(t, []string{"e2e.test/hold"}, r.Finalizers)
	require.JSONEq(t, `{"marker":"m"}`, string(r.Spec))
	require.Nil(t, r.DeletionTimestamp)
}

// TestContract_CreateValidation: дубликат, отсутствующие обязательные поля, битый JSON и
// невалидные лейблы — 400.
func TestContract_CreateValidation(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-valid")
	createWidget(t, cl, shard, name, nil)

	long := strings.Repeat("v", 256)
	cases := []struct {
		name string
		body any
	}{
		{"duplicate", map[string]any{"shard_id": shard, "name": name}},
		{"no shard_id", map[string]any{"name": uniqueName("c-valid")}},
		{"no name", map[string]any{"shard_id": shard}},
		{"label key with =", map[string]any{"shard_id": shard, "name": uniqueName("c-valid"), "labels": map[string]string{"a=b": "v"}}},
		{"label value with =", map[string]any{"shard_id": shard, "name": uniqueName("c-valid"), "labels": map[string]string{"a": "b=c"}}},
		{"empty label key", map[string]any{"shard_id": shard, "name": uniqueName("c-valid"), "labels": map[string]string{"": "v"}}},
		{"label value > 255", map[string]any{"shard_id": shard, "name": uniqueName("c-valid"), "labels": map[string]string{"a": long}}},
	}
	for _, c := range cases {
		code, b, err := cl.Do(http.MethodPost, cl.createPath(), c.body)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, code, "%s: %s", c.name, string(b))
	}

	code, b, err := cl.DoRaw(http.MethodPost, cl.createPath(), []byte(`{"shard_id": `))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, code, "invalid JSON: %s", string(b))
	require.Contains(t, string(b), "Invalid JSON")
}

// TestContract_GetNotFound: GET несуществующего ресурса — 404.
func TestContract_GetNotFound(t *testing.T) {
	t.Parallel()
	_, cl := setupShard(t)
	_, code, err := cl.GetResource(uniqueName("c-missing"))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, code)
}

// TestContract_UpdateVersioning: PUT с совпадающей version — 200 и version+1; с устаревшей — 409
// и ресурс не меняется; без version — без проверки.
func TestContract_UpdateVersioning(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-upd")
	createWidget(t, cl, shard, name, map[string]any{"marker": "v1"})

	r, code, err := cl.UpdateResource(name, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "v2"}), Version: intPtr(1)})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 2, r.Version)
	require.Equal(t, 0, r.CurrentVersion)

	_, code, err = cl.UpdateResource(name, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "stale"}), Version: intPtr(1)})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, code)
	got, _ := getWidget(t, cl, name)
	require.JSONEq(t, `{"marker":"v2"}`, string(got.Spec), "conflicting update must not be applied")
	require.Equal(t, 2, got.Version)

	r, code, err = cl.UpdateResource(name, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "v3"})})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 3, r.Version)

	code, b, err := cl.Do(http.MethodPut, cl.resourcePath(name), map[string]any{"spec": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, code, "shard_id is required: %s", string(b))
}

// TestContract_UpdateLabels: PUT заменяет набор лейблов целиком — добавление, изменение,
// удаление; без поля labels все лейблы снимаются. Изменения видны в list по селектору.
func TestContract_UpdateLabels(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-lbl")
	_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Labels: map[string]string{"keep": "1", "change": "old", "drop": "x"}})
	require.NoError(t, err)

	_, code, err := cl.UpdateResource(name, UpdateBody{ShardID: shard, Labels: map[string]string{"keep": "1", "change": "new", "add": "y"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	r, _ := getWidget(t, cl, name)
	require.Equal(t, map[string]string{"keep": "1", "change": "new", "add": "y"}, r.Labels)

	items, err := cl.List(map[string]string{"shard_id": shard, "label_selector": "change in (new)"})
	require.NoError(t, err)
	require.Equal(t, []string{name}, Names(items))
	items, err = cl.List(map[string]string{"shard_id": shard, "label_selector": "drop in (x)"})
	require.NoError(t, err)
	require.Empty(t, items)

	// PUT — полная замена ресурса: без поля labels у ресурса не остаётся лейблов.
	_, code, err = cl.UpdateResource(name, UpdateBody{ShardID: shard, Spec: mustJSON(map[string]any{"marker": "x"})})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	r, _ = getWidget(t, cl, name)
	require.Empty(t, r.Labels, "PUT without labels removes all labels")
}

// TestContract_UpdateStatus: status не увеличивает version, пишет current_version, status и лейблы;
// устаревшая version — 409, current_version > version — 400.
func TestContract_UpdateStatus(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-status")
	createWidget(t, cl, shard, name, nil)

	r, code, err := cl.UpdateStatus(name, UpdateStatusBody{
		ShardID: shard, Version: 1, CurrentVersion: 1,
		Status: mustJSON(map[string]any{"observedMarker": "s"}),
		Labels: map[string]string{"from": "status"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 1, r.Version, "status update must not bump version")
	require.Equal(t, 1, r.CurrentVersion)

	got, st := getWidget(t, cl, name)
	require.Equal(t, "s", st.ObservedMarker)
	require.Equal(t, map[string]string{"from": "status"}, got.Labels)

	_, code, err = cl.UpdateStatus(name, UpdateStatusBody{ShardID: shard, Version: 7, CurrentVersion: 1})
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, code)

	_, code, err = cl.UpdateStatus(name, UpdateStatusBody{ShardID: shard, Version: 1, CurrentVersion: 2})
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, code, "current_version > version")
}

// TestContract_DeleteWithoutFinalizers: DELETE сразу удаляет строку (204, затем 404).
func TestContract_DeleteWithoutFinalizers(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-del")
	createWidget(t, cl, shard, name, nil)
	require.NoError(t, cl.DeleteResource(name))
	_, code, err := cl.GetResource(name)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, code)
}

// TestContract_DeleteWithFinalizersIdempotent: DELETE ресурса с финализатором ставит
// deletion_timestamp и version+1; повторный DELETE ничего не меняет.
func TestContract_DeleteWithFinalizersIdempotent(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	name := uniqueName("c-delfin")
	_, err := cl.Create(CreateOpts{ShardID: shard, Name: name, Finalizers: []string{"e2e.test/hold"}})
	require.NoError(t, err)

	require.NoError(t, cl.DeleteResource(name))
	first, _ := getWidget(t, cl, name)
	require.NotNil(t, first.DeletionTimestamp)
	require.Equal(t, 2, first.Version)

	require.NoError(t, cl.DeleteResource(name))
	second, _ := getWidget(t, cl, name)
	require.True(t, first.DeletionTimestamp.Equal(*second.DeletionTimestamp), "deletion_timestamp must not move")
	require.Equal(t, 2, second.Version, "repeated DELETE must not bump version")
}

// TestContract_FinalizerRemovalDeletes: у удаляемого ресурса снятие последнего финализатора
// через PUT или PUT /status удаляет строку.
func TestContract_FinalizerRemovalDeletes(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)

	viaPut := uniqueName("c-fin-put")
	viaStatus := uniqueName("c-fin-status")
	for _, n := range []string{viaPut, viaStatus} {
		_, err := cl.Create(CreateOpts{ShardID: shard, Name: n, Finalizers: []string{"e2e.test/hold"}})
		require.NoError(t, err)
		require.NoError(t, cl.DeleteResource(n))
	}

	_, code, err := cl.UpdateResource(viaPut, UpdateBody{ShardID: shard})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	_, code, err = cl.UpdateStatus(viaStatus, UpdateStatusBody{ShardID: shard, Version: 2, CurrentVersion: 2})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)

	for _, n := range []string{viaPut, viaStatus} {
		_, code, err := cl.GetResource(n)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, code, n)
	}

	// Пока финализатор остаётся, ресурс живёт.
	keep := uniqueName("c-fin-keep")
	_, err = cl.Create(CreateOpts{ShardID: shard, Name: keep, Finalizers: []string{"a", "b"}})
	require.NoError(t, err)
	require.NoError(t, cl.DeleteResource(keep))
	_, code, err = cl.UpdateResource(keep, UpdateBody{ShardID: shard, Finalizers: []string{"b"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	r, _ := getWidget(t, cl, keep)
	require.Equal(t, []string{"b"}, r.Finalizers)
	require.NotNil(t, r.DeletionTimestamp)
}

// TestContract_MissingResourceIs404: DELETE / PUT / PUT status несуществующего ресурса — 404
// (так описано в swagger).
//
// Известный баг state-manager: NotFoundError в этих обработчиках не маппится и отдаётся 500.
func TestContract_MissingResourceIs404(t *testing.T) {
	t.Parallel()
	knownBug(t, "state-manager returns 500 instead of 404 for DELETE/PUT of a missing resource")
	shard, cl := setupShard(t)
	name := uniqueName("c-none")
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodDelete, cl.resourcePath(name), nil},
		{http.MethodPut, cl.resourcePath(name), map[string]any{"shard_id": shard}},
		{http.MethodPut, cl.resourcePath(name) + "/status", map[string]any{"shard_id": shard, "version": 1}},
	}
	for _, c := range cases {
		code, b, err := cl.Do(c.method, c.path, c.body)
		require.NoError(t, err)
		require.Equal(t, http.StatusNotFound, code, "%s %s: %s", c.method, c.path, string(b))
	}
}

// TestContract_ListFilters: фильтры name / shard_id / pending и пагинация limit+offset.
func TestContract_ListFilters(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	var names []string
	for i := 0; i < 5; i++ {
		n := uniqueName(fmt.Sprintf("c-list%d", i))
		createWidget(t, cl, shard, n, nil)
		names = append(names, n)
	}
	base := map[string]string{"resource_group": widgetGroup, "kind": widgetKind, "namespace": testNamespace, "shard_id": shard}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}

	all, err := cl.List(base)
	require.NoError(t, err)
	require.ElementsMatch(t, names, Names(all))

	one, err := cl.List(with("name", names[2]))
	require.NoError(t, err)
	require.Equal(t, []string{names[2]}, Names(one))

	// Один ресурс обработан (current_version = version) — он пропадает из pending.
	_, code, err := cl.UpdateStatus(names[0], UpdateStatusBody{ShardID: shard, Version: 1, CurrentVersion: 1})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	pending, err := cl.List(with("pending", "true"))
	require.NoError(t, err)
	require.ElementsMatch(t, names[1:], Names(pending))

	var paged []string
	for offset := 0; offset < 10; offset += 2 {
		page, err := cl.List(with("limit", "2", "offset", fmt.Sprint(offset)))
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), 2)
		paged = append(paged, Names(page)...)
	}
	require.ElementsMatch(t, names, paged, "pages must cover all resources without duplicates")

	other, err := cl.List(with("shard_id", newShard()))
	require.NoError(t, err)
	require.Empty(t, other)
}

// TestContract_LabelSelectors: несколько значений, значения в кавычках, пустое значение, регистр IN,
// пересечение требований, пагинация по селектору.
func TestContract_LabelSelectors(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	mk := func(prefix string, labels map[string]string) string {
		n := uniqueName(prefix)
		_, err := cl.Create(CreateOpts{ShardID: shard, Name: n, Labels: labels})
		require.NoError(t, err)
		return n
	}
	prod := mk("c-sel-prod", map[string]string{"env": "prod", "tier": "web"})
	stage := mk("c-sel-stage", map[string]string{"env": "stage", "tier": "web"})
	dev := mk("c-sel-dev", map[string]string{"env": "dev", "tier": "db"})
	spaced := mk("c-sel-spaced", map[string]string{"env": "a b,c", "tier": "web"})
	empty := mk("c-sel-empty", map[string]string{"env": "", "tier": "db"})

	cases := []struct {
		selector string
		want     []string
	}{
		{"env in (prod,stage)", []string{prod, stage}},
		{"env IN (dev)", []string{dev}},
		{`env in ("a b,c")`, []string{spaced}},
		{`env in ("")`, []string{empty}},
		{"tier in (web),env in (prod,dev)", []string{prod}},
		{"tier in (db)", []string{dev, empty}},
		{"missing in (x)", nil},
	}
	for _, c := range cases {
		items, err := cl.List(map[string]string{"shard_id": shard, "label_selector": c.selector})
		require.NoError(t, err, c.selector)
		require.ElementsMatch(t, c.want, Names(items), c.selector)
	}

	var paged []string
	for offset := 0; offset < 4; offset++ {
		page, err := cl.List(map[string]string{"shard_id": shard, "label_selector": "tier in (web)", "limit": "1", "offset": fmt.Sprint(offset)})
		require.NoError(t, err)
		paged = append(paged, Names(page)...)
	}
	require.ElementsMatch(t, []string{prod, stage, spaced}, paged)
}

// TestContract_LabelSelectorValidation: синтаксическая ошибка, неподдерживаемый оператор и больше 6
// требований — 400.
func TestContract_LabelSelectorValidation(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	tooMany := make([]string, 7)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("k%d in (v)", i)
	}
	for _, sel := range []string{"env in (prod", "env = prod", "env notin (a)", "=x", strings.Join(tooMany, ",")} {
		code, b, err := cl.Do(http.MethodGet, widgetListPath("shard_id", shard, "label_selector", sel), nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, code, "%q: %s", sel, string(b))
	}
	code, _, err := cl.Do(http.MethodGet, widgetListPath("shard_id", shard, "label_selector", strings.Join(tooMany[:6], ",")), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "6 requirements are allowed")
}

// TestContract_SpecialCharsInName: имена с пробелом, кириллицей и спецсимволами проходят через
// PathEscape туда и обратно.
func TestContract_SpecialCharsInName(t *testing.T) {
	t.Parallel()
	shard, cl := setupShard(t)
	for _, base := range []string{"with space", "кириллица", "pct%41", "plus+colon:at@"} {
		name := base + "-" + randHex(3)
		createWidget(t, cl, shard, name, nil)
		r, _ := getWidget(t, cl, name)
		require.Equal(t, name, r.Name)
		items, err := cl.List(map[string]string{"shard_id": shard, "name": name})
		require.NoError(t, err)
		require.Equal(t, []string{name}, Names(items))
		require.NoError(t, cl.DeleteResource(name))
	}
}

// TestContract_Health: liveness и readiness открыты.
func TestContract_Health(t *testing.T) {
	t.Parallel()
	_, cl := setupShard(t)
	raw := rawClient(t)
	for _, p := range []string{"/health/live", "/health/ready"} {
		for _, c := range []*SMClient{cl, raw} {
			code, _, err := c.Do(http.MethodGet, p, nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, code, p)
		}
	}
}
