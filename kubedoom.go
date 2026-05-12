package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func hash(input string) int32 {
	var hash int32
	hash = 5381
	for _, char := range input {
		hash = ((hash << 5) + hash + int32(char))
	}
	if hash < 0 {
		hash = 0 - hash
	}
	return hash
}

func buildKubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

type Mode interface {
	getEntities(ctx context.Context) []string
	deleteEntity(ctx context.Context, entity string)
}

type podmode struct {
	clientset       *kubernetes.Clientset
	namespace       string
	filterNamespace bool
}

func (m podmode) getEntities(ctx context.Context) []string {
	var list *corev1.PodList
	var err error
	if m.filterNamespace {
		list, err = m.clientset.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = m.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		slog.Error("failed to list pods", "error", err)
		return []string{}
	}
	pods := make([]string, 0, len(list.Items))
	for _, pod := range list.Items {
		pods = append(pods, pod.Namespace+"/"+pod.Name)
	}
	return pods
}

func (m podmode) deleteEntity(ctx context.Context, entity string) {
	slog.Info("Pod to kill", "entity", entity)
	podparts := strings.Split(entity, "/")
	if len(podparts) != 2 {
		slog.Error("invalid pod entity", "entity", entity)
		return
	}
	go func() {
		err := m.clientset.CoreV1().Pods(podparts[0]).Delete(context.Background(), podparts[1], metav1.DeleteOptions{})
		if err != nil {
			slog.Error("failed to delete pod", "namespace", podparts[0], "name", podparts[1], "error", err)
		}
	}()
}

type nsmode struct {
	clientset *kubernetes.Clientset
}

func (m nsmode) getEntities(ctx context.Context) []string {
	list, err := m.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		slog.Error("failed to list namespaces", "error", err)
		return []string{}
	}
	namespaces := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return namespaces
}

func (m nsmode) deleteEntity(ctx context.Context, entity string) {
	slog.Info("Namespace to kill", "entity", entity)
	go func() {
		err := m.clientset.CoreV1().Namespaces().Delete(context.Background(), entity, metav1.DeleteOptions{})
		if err != nil {
			slog.Error("failed to delete namespace", "name", entity, "error", err)
		}
	}()
}

func socketLoop(listener net.Listener, mode Mode) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			panic(err)
		}
		stop := false
		for !stop {
			bytes := make([]byte, 40960)
			n, err := conn.Read(bytes)
			if err != nil {
				stop = true
			}
			bytes = bytes[0:n]
			strbytes := strings.TrimSpace(string(bytes))
			entities := mode.getEntities(context.Background())
			if strbytes == "list" {
				for _, entity := range entities {
					padding := strings.Repeat("\n", 255-len(entity))
					_, err = conn.Write([]byte(entity + padding))
					if err != nil {
						slog.Error("Could not write to socket file", "error", err)
						os.Exit(1)
					}
				}
				conn.Close()
				stop = true
			} else if strings.HasPrefix(strbytes, "kill ") {
				parts := strings.Split(strbytes, " ")
				killhash, err := strconv.ParseInt(parts[1], 10, 32)
				if err != nil {
					slog.Error("Could not parse kill hash", "error", err)
					os.Exit(1)
				}
				for _, entity := range entities {
					if hash(entity) == int32(killhash) {
						mode.deleteEntity(context.Background(), entity)
						break
					}
				}
				conn.Close()
				stop = true
			}
		}
	}
}

func main() {
	var modeFlag string
	flag.StringVar(&modeFlag, "mode", "pods", "What to kill pods|namespaces")

	flag.Parse()

	config, err := buildKubeConfig()
	if err != nil {
		slog.Error("failed to build kubeconfig", "error", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Error("failed to create clientset", "error", err)
		os.Exit(1)
	}

	var mode Mode
	switch modeFlag {
	case "pods":
		ns, nsSet := os.LookupEnv("NAMESPACE")
		mode = podmode{clientset: clientset, namespace: ns, filterNamespace: nsSet}
	case "namespaces":
		mode = nsmode{clientset: clientset}
	default:
		slog.Error("Mode should be pods or namespaces")
		os.Exit(1)
	}

	listener, err := net.Listen("unix", "/tmp/dockerdoom.socket")
	if err != nil {
		slog.Error("Could not create socket file", "error", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	slog.Info("KubeDoom socket server started", "socket", "/tmp/dockerdoom.socket")

	go socketLoop(listener, mode)

	<-sigCh
	slog.Info("Shutting down...")
	listener.Close()
	time.Sleep(2 * time.Second)
}
