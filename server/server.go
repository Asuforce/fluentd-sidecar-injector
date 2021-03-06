package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/kelseyhightower/envconfig"
	webhookhttp "github.com/slok/kubewebhook/pkg/http"
	"github.com/slok/kubewebhook/pkg/log"
	"github.com/slok/kubewebhook/pkg/webhook/mutating"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var annotationPrefix = "fluentd-sidecar-injector.h3poteto.dev"

// Env is required environment variables to run this server.
type Env struct {
	DockerImage       string `envconfig:"DOCKER_IMAGE" default:"h3poteto/fluentd-forward:latest"`
	ApplicationLogDir string `envconfig:"APPLICATION_LOG_DIR"`
	TimeFormat        string `envconfig:"TIME_FORMAT" default:"%Y-%m-%dT%H:%M:%S%z"`
	TimeKey           string `envconfig:"TIME_KEY" default:"time"`
	TagPrefix         string `envconfig:"TAG_PREFIX" default:"app"`
	AggregatorHost    string `envconfig:"AGGREGATOR_HOST"`
	AggregatorPort    string `envconfig:"AGGREGATOR_PORT" default:"24224"`
}

// StartServer run webhook server.
func StartServer(tlsCertFile, tlsKeyFile string) error {
	logger := &log.Std{Debug: true}

	mutator := mutating.MutatorFunc(sidecarInjectMutator)

	config := mutating.WebhookConfig{
		Name: "fluentdSidecarInjector",
		Obj:  &corev1.Pod{},
	}
	webhook, err := mutating.NewWebhook(config, mutator, nil, nil, logger)
	if err != nil {
		return fmt.Errorf("Failed to create webhook: %s", err)
	}

	handler, err := webhookhttp.HandlerFor(webhook)
	if err != nil {
		return fmt.Errorf("Failed to create webhook handler: %s", err)
	}

	logger.Infof("Listing on :8080")
	err = http.ListenAndServeTLS(":8080", tlsCertFile, tlsKeyFile, handler)
	if err != nil {
		return fmt.Errorf("Failed to start server: %s", err)
	}

	return nil

}

// sidecarInjectMutator mutates requested pod definition to inject fluentd as sidecar.
func sidecarInjectMutator(_ context.Context, obj metav1.Object) (stop bool, err error) {
	pod, ok := obj.(*corev1.Pod)

	if !ok {
		return false, nil
	}

	if pod.Annotations[annotationPrefix+"/injection"] != "enabled" {
		return false, nil
	}

	var fluentdEnv Env
	envconfig.Process("fluentd", &fluentdEnv)

	dockerImage := fluentdEnv.DockerImage
	if value, ok := pod.Annotations[annotationPrefix+"/docker-image"]; ok {
		dockerImage = value
	}

	volumeName := "fluentd-sidecar-injector-logs"
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})

	sidecar := corev1.Container{
		Name:  "fluentd-sidecar",
		Image: dockerImage,
		Resources: corev1.ResourceRequirements{
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: *resource.NewQuantity(200*1024*1024, resource.BinarySI),
				corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
			},
			Limits: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceMemory: *resource.NewQuantity(1000*1024*1024, resource.BinarySI),
			},
		},
	}

	// Override env with Pod's annotations.
	sendTimeout := "60s"
	if value, ok := pod.Annotations[annotationPrefix+"/send-timeout"]; ok {
		sendTimeout = value
	}
	sidecar.Env = append(sidecar.Env, corev1.EnvVar{
		Name:  "SEND_TIMEOUT",
		Value: sendTimeout,
	})

	recoverWait := "10s"
	if value, ok := pod.Annotations[annotationPrefix+"/recover-wait"]; ok {
		recoverWait = value
	}
	sidecar.Env = append(sidecar.Env, corev1.EnvVar{
		Name:  "RECOVER_WAIT",
		Value: recoverWait,
	})

	hardTimeout := "120s"
	if value, ok := pod.Annotations[annotationPrefix+"/hard-timeout"]; ok {
		hardTimeout = value
	}
	sidecar.Env = append(sidecar.Env, corev1.EnvVar{
		Name:  "HARD_TIMEOUT",
		Value: hardTimeout,
	})

	// Override env with fluentdEnv and Pod's annotations.
	aggregatorHost := fluentdEnv.AggregatorHost
	if value, ok := pod.Annotations[annotationPrefix+"/aggregator-host"]; ok {
		aggregatorHost = value
	}

	if len(aggregatorHost) == 0 {
		return false, errors.New("aggregator host is required")
	}
	sidecar.Env = append(sidecar.Env, corev1.EnvVar{
		Name:  "AGGREGATOR_HOST",
		Value: aggregatorHost,
	})

	aggregatorPort := fluentdEnv.AggregatorPort
	if value, ok := pod.Annotations[annotationPrefix+"/aggregator-port"]; ok {
		aggregatorPort = value
	}

	if len(aggregatorPort) > 0 {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{
			Name:  "AGGREGATOR_PORT",
			Value: aggregatorPort,
		})
	}

	applicationLogDir := fluentdEnv.ApplicationLogDir
	if value, ok := pod.Annotations[annotationPrefix+"/application-log-dir"]; ok {
		applicationLogDir = value
	}
	if len(applicationLogDir) == 0 {
		return false, errors.New("application log dir is required")
	}
	sidecar.Env = append(sidecar.Env, corev1.EnvVar{
		Name:  "APPLICATION_LOG_DIR",
		Value: applicationLogDir,
	})

	volumeMount := corev1.VolumeMount{
		Name:      volumeName,
		ReadOnly:  false,
		MountPath: applicationLogDir,
	}
	sidecar.VolumeMounts = []corev1.VolumeMount{
		volumeMount,
	}

	tagPrefix := fluentdEnv.TagPrefix
	if value, ok := pod.Annotations[annotationPrefix+"/tag-prefix"]; ok {
		tagPrefix = value
	}
	if len(tagPrefix) > 0 {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{
			Name:  "TAG_PREFIX",
			Value: tagPrefix,
		})
	}

	timeKey := fluentdEnv.TimeKey
	if value, ok := pod.Annotations[annotationPrefix+"/time-key"]; ok {
		timeKey = value
	}
	if len(timeKey) > 0 {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{
			Name:  "TIME_KEY",
			Value: timeKey,
		})
	}

	timeFormat := fluentdEnv.TimeFormat
	if value, ok := pod.Annotations[annotationPrefix+"/time-format"]; ok {
		timeFormat = value
	}
	if len(timeFormat) > 0 {
		sidecar.Env = append(sidecar.Env, corev1.EnvVar{
			Name:  "TIME_FORMAT",
			Value: timeFormat,
		})
	}

	// Inject volume mount for all containers in the pod.
	var containers []corev1.Container

	for _, container := range pod.Spec.Containers {
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
		containers = append(containers, container)
	}
	containers = append(containers, sidecar)

	pod.Spec.Containers = containers

	return false, nil
}
