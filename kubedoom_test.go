package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rewriteTransport redirects all requests to a target URL while preserving
// the original path and query.  This lets us point a *kubernetes.Clientset
// at an httptest.Server without worrying about TLS or host mismatch.
type rewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return t.base.RoundTrip(req)
}

// newRealClientsetWithFakeObjects returns a real *kubernetes.Clientset backed
// by a fake.Clientset served over a local HTTP server.  The returned cleanup
// function must be called when the clientset is no longer needed.
func newRealClientsetWithFakeObjects(objects ...runtime.Object) (*kubernetes.Clientset, func()) {
	fakeClient := fake.NewSimpleClientset(objects...)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)

	// Single catch-all handler for the small API surface we need.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		method := r.Method

		w.Header().Set("Content-Type", "application/json")

		switch {
		// List all pods.
		case path == "/api/v1/pods" && method == http.MethodGet:
			list, err := fakeClient.CoreV1().Pods("").List(context.Background(), metav1.ListOptions{})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(list)
			return

		// List all namespaces.
		case path == "/api/v1/namespaces" && method == http.MethodGet:
			list, err := fakeClient.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}

		// Paths like /api/v1/namespaces/{ns}/pods or /api/v1/namespaces/{ns}/pods/{name}
		// or /api/v1/namespaces/{ns}
		if strings.HasPrefix(path, "/api/v1/namespaces/") {
			rest := strings.TrimPrefix(path, "/api/v1/namespaces/")
			parts := strings.Split(rest, "/")

			// Delete namespace.
			if len(parts) == 1 && method == http.MethodDelete {
				_ = fakeClient.CoreV1().Namespaces().Delete(context.Background(), parts[0], metav1.DeleteOptions{})
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusSuccess})
				return
			}

			// List pods in a namespace.
			if len(parts) == 2 && parts[1] == "pods" && method == http.MethodGet {
				list, err := fakeClient.CoreV1().Pods(parts[0]).List(context.Background(), metav1.ListOptions{})
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(list)
				return
			}

			// Delete pod.
			if len(parts) == 3 && parts[1] == "pods" && method == http.MethodDelete {
				_ = fakeClient.CoreV1().Pods(parts[0]).Delete(context.Background(), parts[2], metav1.DeleteOptions{})
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusSuccess})
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	})

	target, _ := url.Parse(server.URL)
	transport := &rewriteTransport{
		base:   &http.Transport{},
		target: target,
	}
	httpClient := &http.Client{Transport: transport}

	// Host is ignored by rewriteTransport but required by NewForConfigAndClient.
	config := &rest.Config{Host: "https://localhost"}
	cs, err := kubernetes.NewForConfigAndClient(config, httpClient)
	if err != nil {
		server.Close()
		panic(err)
	}

	return cs, server.Close
}

// ---------------------------------------------------------------------------
// 4.1 Unit tests for pure logic (hash)
// ---------------------------------------------------------------------------

func TestHash_EmptyString(t *testing.T) {
	t.Parallel()
	result := hash("")
	if result != 5381 {
		t.Fatalf("expected hash(\"\") = 5381, got %d", result)
	}
}

func TestHash_SingleCharacter(t *testing.T) {
	t.Parallel()
	// djb2: h = ((h << 5) + h + char) for each rune
	// For 'a' (97): ((5381 << 5) + 5381 + 97) = (172192 + 5381 + 97) = 177670
	expected := int32((5381 << 5) + 5381 + 97)
	result := hash("a")
	if result != expected {
		t.Fatalf("expected hash(\"a\") = %d, got %d", expected, result)
	}
}

func TestHash_TypicalPodNames(t *testing.T) {
	t.Parallel()
	testCases := []string{
		"default/my-pod-123",
		"kube-system/coredns-abc456-def",
		"my-namespace/some-really-long-pod-name-that-might-exist-in-a-cluster",
	}
	for _, tc := range testCases {
		result1 := hash(tc)
		result2 := hash(tc)
		if result1 != result2 {
			t.Fatalf("hash not deterministic for %q: %d vs %d", tc, result1, result2)
		}
	}
}

func TestHash_Unicode(t *testing.T) {
	t.Parallel()
	testCases := []string{
		"日本語/ポッド-1",
		"emoji-😀-pod",
		"mixed-αβγ-namespace/δεζ-pod",
	}
	for _, tc := range testCases {
		result1 := hash(tc)
		result2 := hash(tc)
		if result1 != result2 {
			t.Fatalf("hash not deterministic for %q: %d vs %d", tc, result1, result2)
		}
	}
}

func TestHash_Deterministic(t *testing.T) {
	t.Parallel()
	input := "kube-system/kube-proxy-7x9k2"
	var results []int32
	for i := 0; i < 100; i++ {
		results = append(results, hash(input))
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("hash not deterministic on iteration %d: %d vs %d", i, results[i], results[0])
		}
	}
}

func TestHash_DifferentInputs(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"default/pod-a",
		"default/pod-b",
		"kube-system/pod-a",
		"",
		"a",
		"ab",
		"abc",
	}
	seen := make(map[int32]string, len(inputs))
	for _, in := range inputs {
		h := hash(in)
		if other, ok := seen[h]; ok {
			t.Fatalf("collision between %q and %q: hash=%d", other, in, h)
		}
		seen[h] = in
	}
}

// ---------------------------------------------------------------------------
// 4.2 Tests with fake clientset
// ---------------------------------------------------------------------------

func TestPodMode_GetEntities_AllNamespaces(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-3", Namespace: "kube-system"}},
	)
	defer cleanup()

	m := podmode{clientset: client, filterNamespace: false}
	entities := m.getEntities(context.Background())

	if len(entities) != 3 {
		t.Fatalf("expected 3 pods, got %d: %v", len(entities), entities)
	}
	want := map[string]bool{
		"default/pod-1":     true,
		"default/pod-2":     true,
		"kube-system/pod-3": true,
	}
	for _, e := range entities {
		if !want[e] {
			t.Fatalf("unexpected entity %q", e)
		}
	}
}

func TestPodMode_GetEntities_FilteredNamespace(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-3", Namespace: "kube-system"}},
	)
	defer cleanup()

	m := podmode{clientset: client, namespace: "default", filterNamespace: true}
	entities := m.getEntities(context.Background())

	if len(entities) != 2 {
		t.Fatalf("expected 2 pods, got %d: %v", len(entities), entities)
	}
	for _, e := range entities {
		if !strings.HasPrefix(e, "default/") {
			t.Fatalf("expected only default namespace pods, got %q", e)
		}
	}
}

func TestPodMode_GetEntities_Empty(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects()
	defer cleanup()

	m := podmode{clientset: client, filterNamespace: false}
	entities := m.getEntities(context.Background())
	if len(entities) != 0 {
		t.Fatalf("expected 0 pods, got %d: %v", len(entities), entities)
	}
}

func TestPodMode_DeleteEntity(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2", Namespace: "default"}},
	)
	defer cleanup()

	m := podmode{clientset: client}
	m.deleteEntity(context.Background(), "default/pod-1")

	// Wait for the goroutine spawned by deleteEntity to finish.
	time.Sleep(300 * time.Millisecond)

	pods, err := client.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 pod remaining, got %d", len(pods.Items))
	}
	if pods.Items[0].Name != "pod-2" {
		t.Fatalf("expected remaining pod to be pod-2, got %s", pods.Items[0].Name)
	}
}

func TestPodMode_DeleteEntity_InvalidFormat(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}},
	)
	defer cleanup()

	m := podmode{clientset: client}
	// Should not panic and should not delete anything.
	m.deleteEntity(context.Background(), "invalid-entity-no-slash")
	m.deleteEntity(context.Background(), "too/many/slashes")

	time.Sleep(300 * time.Millisecond)

	pods, err := client.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 pod remaining, got %d", len(pods.Items))
	}
}

func TestNsMode_GetEntities(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app-ns"}},
	)
	defer cleanup()

	m := nsmode{clientset: client}
	entities := m.getEntities(context.Background())

	if len(entities) != 3 {
		t.Fatalf("expected 3 namespaces, got %d: %v", len(entities), entities)
	}
	want := map[string]bool{"default": true, "kube-system": true, "app-ns": true}
	for _, e := range entities {
		if !want[e] {
			t.Fatalf("unexpected namespace %q", e)
		}
	}
}

func TestNsMode_GetEntities_Empty(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects()
	defer cleanup()

	m := nsmode{clientset: client}
	entities := m.getEntities(context.Background())
	if len(entities) != 0 {
		t.Fatalf("expected 0 namespaces, got %d: %v", len(entities), entities)
	}
}

func TestNsMode_DeleteEntity(t *testing.T) {
	t.Parallel()
	client, cleanup := newRealClientsetWithFakeObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
	)
	defer cleanup()

	m := nsmode{clientset: client}
	m.deleteEntity(context.Background(), "default")

	time.Sleep(300 * time.Millisecond)

	nsList, err := client.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list namespaces: %v", err)
	}
	if len(nsList.Items) != 1 {
		t.Fatalf("expected 1 namespace remaining, got %d", len(nsList.Items))
	}
	if nsList.Items[0].Name != "kube-system" {
		t.Fatalf("expected remaining namespace to be kube-system, got %s", nsList.Items[0].Name)
	}
}

// ---------------------------------------------------------------------------
// 4.3 Socket protocol integration tests
// ---------------------------------------------------------------------------

// testMode is a mock implementation of Mode for socket tests.
type testMode struct {
	mu        sync.Mutex
	entities  []string
	deleted   []string
	listCalls int
}

func (m *testMode) getEntities(_ context.Context) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listCalls++
	return append([]string{}, m.entities...)
}

func (m *testMode) deleteEntity(_ context.Context, entity string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, entity)
}

func startTestSocketServer(t *testing.T, mode Mode) (socketPath string, cleanup func()) {
	t.Helper()
	tmpDir := t.TempDir()
	socketPath = filepath.Join(tmpDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create unix listener: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		socketLoop(listener, mode)
	}()

	cleanup = func() {
		listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Log("socketLoop did not exit in time")
		}
	}
	return socketPath, cleanup
}

func TestSocketLoop_ListCommand(t *testing.T) {
	t.Parallel()
	mode := &testMode{entities: []string{"default/pod-1", "kube-system/pod-2"}}
	path, cleanup := startTestSocketServer(t, mode)
	defer cleanup()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("failed to dial socket: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("list\n"))
	if err != nil {
		t.Fatalf("failed to write list command: %v", err)
	}

	// Each entity is padded with newlines to exactly 255 bytes.
	var entities []string
	for {
		buf := make([]byte, 255)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			break
		}
		// Only keep the actual bytes received (may be less than 255 on last read,
		// but the server writes full 255-byte chunks and then closes).
		entity := strings.TrimSpace(string(buf[:n]))
		if entity != "" {
			entities = append(entities, entity)
		}
	}

	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d: %v", len(entities), entities)
	}
	if entities[0] != "default/pod-1" {
		t.Fatalf("expected first entity default/pod-1, got %q", entities[0])
	}
	if entities[1] != "kube-system/pod-2" {
		t.Fatalf("expected second entity kube-system/pod-2, got %q", entities[1])
	}
}

func TestSocketLoop_KillCommand(t *testing.T) {
	t.Parallel()
	mode := &testMode{entities: []string{"default/pod-1", "kube-system/pod-2"}}
	path, cleanup := startTestSocketServer(t, mode)
	defer cleanup()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("failed to dial socket: %v", err)
	}

	target := "kube-system/pod-2"
	killHash := hash(target)
	cmd := "kill " + strconv.FormatInt(int64(killHash), 10) + "\n"
	_, err = conn.Write([]byte(cmd))
	if err != nil {
		conn.Close()
		t.Fatalf("failed to write kill command: %v", err)
	}

	// Wait for server to process and close.
	buf := make([]byte, 1)
	_, _ = conn.Read(buf)
	conn.Close()

	// Give the socketLoop a moment to call deleteEntity.
	time.Sleep(100 * time.Millisecond)

	mode.mu.Lock()
	defer mode.mu.Unlock()
	if len(mode.deleted) != 1 {
		t.Fatalf("expected 1 deleted entity, got %d: %v", len(mode.deleted), mode.deleted)
	}
	if mode.deleted[0] != target {
		t.Fatalf("expected deleted entity %q, got %q", target, mode.deleted[0])
	}
}

func TestSocketLoop_UnknownCommand(t *testing.T) {
	t.Parallel()
	mode := &testMode{entities: []string{"default/pod-1"}}
	path, cleanup := startTestSocketServer(t, mode)
	defer cleanup()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("failed to dial socket: %v", err)
	}
	defer conn.Close()

	// Send an unknown command.
	_, err = conn.Write([]byte("unknown\n"))
	if err != nil {
		t.Fatalf("failed to write unknown command: %v", err)
	}

	// Wait a bit; the connection should stay open because unknown commands are ignored.
	time.Sleep(100 * time.Millisecond)

	mode.mu.Lock()
	callsBefore := mode.listCalls
	mode.mu.Unlock()

	// Now send a valid list command on the same connection.
	_, err = conn.Write([]byte("list\n"))
	if err != nil {
		t.Fatalf("failed to write list command after unknown: %v", err)
	}

	// Read response.
	var entities []string
	for {
		buf := make([]byte, 255)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			break
		}
		entity := strings.TrimSpace(string(buf[:n]))
		if entity != "" {
			entities = append(entities, entity)
		}
	}

	if len(entities) != 1 || entities[0] != "default/pod-1" {
		t.Fatalf("expected [default/pod-1], got %v", entities)
	}

	// listCalls should have increased at least twice: once for unknown, once for list.
	mode.mu.Lock()
	if mode.listCalls < callsBefore+1 {
		t.Fatalf("expected listCalls to increase, got %d (was %d)", mode.listCalls, callsBefore)
	}
	mode.mu.Unlock()
}
