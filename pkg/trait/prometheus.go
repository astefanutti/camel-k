/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package trait

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/apache/camel-k/deploy"
	v1 "github.com/apache/camel-k/pkg/apis/camel/v1"
	"github.com/apache/camel-k/pkg/util"
)

// The Prometheus trait configures a Prometheus-compatible endpoint. It also creates a `PodMonitor` resource,
// so that the endpoint can be scraped automatically, when using the Prometheus operator.
//
// The metrics exposed vary depending on the configured runtime. With Quarkus, the metrics are exposed
// using MicroProfile Metrics. While with the default runtime, they are exposed using the Prometheus JMX exporter.
//
// WARNING: The creation of the `PodMonitor` resource requires the https://github.com/coreos/prometheus-operator[Prometheus Operator]
// custom resource definition to be installed.
// You can set `pod-monitor` to `false` for the Prometheus trait to work without the Prometheus operator CRDs.
//
// It's disabled by default.
//
// +camel-k:trait=prometheus
type prometheusTrait struct {
	BaseTrait `property:",squash"`
	// The Prometheus endpoint port (default `9779`, or `8080` with Quarkus).
	Port *int `property:"port"`
	// Whether a `PodMonitor` resource is created (default `true`).
	PodMonitor bool `property:"pod-monitor"`
	// The `PodMonitor` resource labels, applicable when `pod-monitor` is `true`.
	PodMonitorLabels string `property:"pod-monitor-labels"`
	// To use a custom ConfigMap containing the Prometheus JMX exporter configuration (under the `content` ConfigMap key).
	// When this property is left empty (default), Camel K generates a standard Prometheus configuration for the integration.
	// It is not applicable when using Quarkus.
	ConfigMap string `property:"configmap"`
}

const (
	prometheusJmxExporterConfigFileName  = "prometheus-jmx-exporter.yaml"
	prometheusJmxExporterConfigMountPath = "/etc/prometheus"
)

func newPrometheusTrait() Trait {
	return &prometheusTrait{
		BaseTrait:  NewBaseTrait("prometheus", 1900),
		PodMonitor: true,
	}
}

func (t *prometheusTrait) Configure(e *Environment) (bool, error) {
	return t.Enabled != nil && *t.Enabled && e.IntegrationInPhase(
		v1.IntegrationPhaseInitialization,
		v1.IntegrationPhaseDeploying,
		v1.IntegrationPhaseRunning,
	), nil
}

func (t *prometheusTrait) Apply(e *Environment) (err error) {
	if e.IntegrationInPhase(v1.IntegrationPhaseInitialization) {
		switch e.CamelCatalog.Runtime.Provider {
		case v1.RuntimeProviderQuarkus:
			// Add the Camel Quarkus MP Metrics extension
			util.StringSliceUniqueAdd(&e.Integration.Status.Dependencies, "mvn:org.apache.camel.quarkus/camel-quarkus-microprofile-metrics")
		case v1.RuntimeProviderMain:
			// Add the Camel management and Prometheus agent dependencies
			util.StringSliceUniqueAdd(&e.Integration.Status.Dependencies, "mvn:org.apache.camel/camel-management")
			// TODO: We may want to make the Prometheus version configurable
			util.StringSliceUniqueAdd(&e.Integration.Status.Dependencies, "mvn:io.prometheus.jmx/jmx_prometheus_javaagent:0.3.1")

			// Use the provided configuration or add the default Prometheus JMX exporter configuration
			configMapName := t.getJmxExporterConfigMapOrAdd(e)

			e.Integration.Status.AddOrReplaceGeneratedResources(v1.ResourceSpec{
				Type: v1.ResourceTypeData,
				DataSpec: v1.DataSpec{
					Name:       prometheusJmxExporterConfigFileName,
					ContentRef: configMapName,
				},
				MountPath: prometheusJmxExporterConfigMountPath,
			})
		}
		return nil
	}

	container := e.getIntegrationContainer()
	if container == nil {
		e.Integration.Status.SetCondition(
			v1.IntegrationConditionPrometheusAvailable,
			corev1.ConditionFalse,
			v1.IntegrationConditionContainerNotAvailableReason,
			"",
		)
		return nil
	}

	condition := v1.IntegrationCondition{
		Type:   v1.IntegrationConditionPrometheusAvailable,
		Status: corev1.ConditionTrue,
		Reason: v1.IntegrationConditionPrometheusAvailableReason,
	}

	var port int
	switch e.CamelCatalog.Runtime.Provider {
	case v1.RuntimeProviderQuarkus:
		port = 8080
	case v1.RuntimeProviderMain:
		port = 9779
		// Configure the Prometheus Java agent
		options := []string{strconv.Itoa(port), path.Join(prometheusJmxExporterConfigMountPath, prometheusJmxExporterConfigFileName)}
		container.Args = append(container.Args, "-javaagent:dependencies/io.prometheus.jmx.jmx_prometheus_javaagent-0.3.1.jar="+strings.Join(options, ":"))
	}

	if t.Port == nil {
		t.Port = &port
	}

	// Configure the Prometheus container port
	containerPort := t.getContainerPort()
	controller, err := e.DetermineControllerStrategy()
	if err != nil {
		return err
	}
	// Skip declaring the Prometheus port when Knative is enabled, as only one container port is supported
	if controller != ControllerStrategyKnativeService {
		container.Ports = append(container.Ports, *containerPort)
	}
	condition.Message = fmt.Sprintf("%s(%d)", container.Name, containerPort.ContainerPort)

	// Add the PodMonitor resource
	if t.PodMonitor {
		podMonitor, err := t.getPodMonitorFor(e)
		if err != nil {
			return err
		}
		e.Resources.Add(podMonitor)
		condition.Message = fmt.Sprintf("PodMonitor (%s) -> ", podMonitor.Name) + condition.Message
	} else {
		condition.Message = "ContainerPort " + condition.Message
	}

	e.Integration.Status.SetConditions(condition)

	return nil
}

func (t *prometheusTrait) getContainerPort() *corev1.ContainerPort {
	containerPort := corev1.ContainerPort{
		ContainerPort: int32(*t.Port),
		Protocol:      corev1.ProtocolTCP,
	}
	return &containerPort
}

func (t *prometheusTrait) getPodMonitorFor(e *Environment) (*monitoringv1.PodMonitor, error) {
	labels, err := parseCsvMap(&t.PodMonitorLabels)
	if err != nil {
		return nil, err
	}
	labels["camel.apache.org/integration"] = e.Integration.Name

	targetPort := intstr.FromInt(*t.Port)

	podMonitor := monitoringv1.PodMonitor{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodMonitor",
			APIVersion: "monitoring.coreos.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      e.Integration.Name,
			Namespace: e.Integration.Namespace,
			Labels:    labels,
		},
		Spec: monitoringv1.PodMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"camel.apache.org/integration": e.Integration.Name,
				},
			},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					// Avoid relying on named port, as Knative enforces specific values used for content negotiation
					TargetPort: &targetPort,
				},
			},
		},
	}
	return &podMonitor, nil
}

func (t *prometheusTrait) getJmxExporterConfigMapOrAdd(e *Environment) string {
	if t.ConfigMap != "" {
		return t.ConfigMap
	}

	// Add a default config if not specified by the user
	defaultName := e.Integration.Name + "-prometheus"
	cm := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultName,
			Namespace: e.Integration.Namespace,
			Labels: map[string]string{
				"camel.apache.org/integration": e.Integration.Name,
			},
		},
		Data: map[string]string{
			"content": deploy.ResourceAsString("/prometheus-jmx-exporter.yaml"),
		},
	}
	e.Resources.Add(&cm)
	return defaultName
}
