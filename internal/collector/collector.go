package collector

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mlhiter/sealos-collector/internal/config"
	"github.com/mlhiter/sealos-collector/internal/status"
)

type Collector struct {
	cfg     *config.Config
	kube    kubernetes.Interface
	kubeErr error
	state   *StateStore
}

const maxWarningSamples = 3

var (
	publicEventURLPattern         = regexp.MustCompile(`https?://[^\s"]+`)
	publicEventSensitiveKVPattern = regexp.MustCompile(`(?i)(token|password|secret|key)=([^&\s]+)`)
)

func New(cfg *config.Config) *Collector {
	collector, _ := NewWithState(cfg, "")
	return collector
}

func NewWithState(cfg *config.Config, statePath string) (*Collector, error) {
	stateStore, err := LoadStateStore(statePath)
	if err != nil {
		return nil, err
	}
	restCfg, err := buildKubeConfig(cfg)
	if err != nil {
		return &Collector{cfg: cfg, kubeErr: err, state: stateStore}, nil
	}
	client, err := kubernetes.NewForConfig(restCfg)
	return &Collector{cfg: cfg, kube: client, kubeErr: err, state: stateStore}, nil
}

func (c *Collector) Collect(ctx context.Context) (*status.Snapshot, error) {
	components := make([]status.Component, 0, len(c.cfg.Components))
	overall := status.Operational
	configuredKeys := configuredCheckKeys(c.cfg.Components)

	for _, componentConfig := range c.cfg.Components {
		component := c.collectComponent(ctx, componentConfig)
		components = append(components, component)
		overall = status.Worse(overall, component.Status)
	}

	snapshot := &status.Snapshot{
		Version: "v1",
		Cluster: status.Cluster{
			ID:   c.cfg.Cluster.ID,
			Name: c.cfg.Cluster.Name,
		},
		GeneratedAt:   time.Now().UTC(),
		OverallStatus: overall,
		Components:    components,
	}

	if !c.cfg.Publish.IncludeCheckDetails {
		for i := range snapshot.Components {
			snapshot.Components[i].Checks = nil
		}
	}
	if c.state != nil {
		c.state.Prune(configuredKeys)
		if err := c.state.Save(); err != nil {
			return nil, fmt.Errorf("save state: %w", err)
		}
	}

	return snapshot, nil
}

func configuredCheckKeys(components []config.ComponentConfig) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, component := range components {
		for _, check := range component.Checks {
			if component.ID == "" || check.ID == "" {
				continue
			}
			keys[checkStateKey(component.ID, check.ID)] = struct{}{}
		}
	}
	return keys
}

func (c *Collector) collectComponent(ctx context.Context, componentConfig config.ComponentConfig) status.Component {
	results := make([]status.CheckResult, 0, len(componentConfig.Checks))
	componentStatus := status.Unknown
	if len(componentConfig.Checks) > 0 {
		componentStatus = status.Operational
	}

	for _, checkConfig := range componentConfig.Checks {
		result := c.runCheck(ctx, checkConfig)
		result = c.stabilizeCheck(componentConfig.ID, result)
		results = append(results, result)
		componentStatus = status.Worse(componentStatus, componentStatusForCheck(result))
	}

	return status.Component{
		ID:           componentConfig.ID,
		Name:         componentConfig.Name,
		Group:        componentConfig.Group,
		Description:  componentConfig.Description,
		UserFacing:   componentConfig.UserFacing,
		Status:       componentStatus,
		Summary:      summarize(componentStatus, results),
		PublicChecks: publicCheckResults(componentConfig, componentStatus, results),
		Checks:       results,
	}
}

func (c *Collector) runCheck(ctx context.Context, check config.CheckConfig) status.CheckResult {
	started := time.Now()
	result := status.CheckResult{
		ID:         check.ID,
		Name:       check.Name,
		Type:       check.Type,
		Impact:     config.NormalizeCheckImpact(check.Impact),
		Status:     status.Unknown,
		ObservedAt: started.UTC(),
		Metadata:   map[string]string{},
	}

	var statusLevel status.Level
	var message string
	var metadata map[string]string

	switch strings.ToLower(check.Type) {
	case "workload":
		statusLevel, message, metadata = c.checkWorkload(ctx, check)
	case "serviceendpoints":
		statusLevel, message, metadata = c.checkServiceEndpoints(ctx, check)
	case "http":
		statusLevel, message, metadata = c.checkHTTP(ctx, check)
	case "kubernetesreadyz":
		statusLevel, message, metadata = c.checkKubernetesReadyz(ctx, check)
	case "prometheusquery":
		statusLevel, message, metadata = c.checkPrometheusQuery(ctx, check)
	case "recentwarnings":
		statusLevel, message, metadata = c.checkRecentWarnings(ctx, check)
	default:
		statusLevel = status.Unknown
		message = fmt.Sprintf("unsupported check type %q", check.Type)
	}

	result.Status = statusLevel
	result.Message = message
	for key, value := range metadata {
		result.Metadata[key] = value
	}
	if len(result.Metadata) == 0 {
		result.Metadata = nil
	}
	result.DurationMS = time.Since(started).Milliseconds()

	return result
}

func (c *Collector) stabilizeCheck(componentID string, result status.CheckResult) status.CheckResult {
	if c.state == nil {
		return result
	}
	key := checkStateKey(componentID, result.ID)
	return c.state.StabilizeUnknown(key, result, c.cfg.StatusPolicy, time.Now().UTC())
}

func checkStateKey(componentID, checkID string) string {
	return componentID + "/" + checkID
}

func (c *Collector) checkWorkload(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	if c.kube == nil {
		return status.Unknown, fmt.Sprintf("kubernetes client unavailable: %v", c.kubeErr), nil
	}
	if check.Namespace == "" || check.ResourceName == "" || check.Kind == "" {
		return status.Unknown, "workload check requires namespace, kind, and resourceName", nil
	}

	var desired int32
	var ready int32
	var err error

	switch strings.ToLower(check.Kind) {
	case "deployment":
		var obj *appsv1.Deployment
		obj, err = c.kube.AppsV1().Deployments(check.Namespace).Get(ctx, check.ResourceName, metav1.GetOptions{})
		if err == nil {
			desired = int32(1)
			if obj.Spec.Replicas != nil {
				desired = *obj.Spec.Replicas
			}
			ready = obj.Status.AvailableReplicas
		}
	case "statefulset":
		var obj *appsv1.StatefulSet
		obj, err = c.kube.AppsV1().StatefulSets(check.Namespace).Get(ctx, check.ResourceName, metav1.GetOptions{})
		if err == nil {
			desired = int32(1)
			if obj.Spec.Replicas != nil {
				desired = *obj.Spec.Replicas
			}
			ready = obj.Status.ReadyReplicas
		}
	case "daemonset":
		var obj *appsv1.DaemonSet
		obj, err = c.kube.AppsV1().DaemonSets(check.Namespace).Get(ctx, check.ResourceName, metav1.GetOptions{})
		if err == nil {
			desired = obj.Status.DesiredNumberScheduled
			ready = obj.Status.NumberReady
		}
	default:
		return status.Unknown, fmt.Sprintf("unsupported workload kind %q", check.Kind), nil
	}

	if err != nil {
		if apierrors.IsNotFound(err) {
			return status.Outage, "workload not found", workloadMetadata(check, desired, ready)
		}
		return status.Unknown, fmt.Sprintf("failed to read workload: %v", err), workloadMetadata(check, desired, ready)
	}

	minReady := int32(check.MinReady)
	if minReady <= 0 {
		minReady = desired
	}

	metadata := workloadMetadata(check, desired, ready)
	metadata["minReady"] = strconv.Itoa(int(minReady))

	switch {
	case desired == 0:
		return status.Degraded, "workload has zero desired replicas", metadata
	case ready >= minReady:
		return status.Operational, fmt.Sprintf("%d/%d replicas ready", ready, desired), metadata
	case ready > 0:
		return status.Degraded, fmt.Sprintf("%d/%d replicas ready", ready, desired), metadata
	default:
		return status.Outage, fmt.Sprintf("%d/%d replicas ready", ready, desired), metadata
	}
}

func workloadMetadata(check config.CheckConfig, desired, ready int32) map[string]string {
	return map[string]string{
		"namespace": check.Namespace,
		"kind":      check.Kind,
		"resource":  check.ResourceName,
		"desired":   strconv.Itoa(int(desired)),
		"ready":     strconv.Itoa(int(ready)),
	}
}

func (c *Collector) checkServiceEndpoints(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	if c.kube == nil {
		return status.Unknown, fmt.Sprintf("kubernetes client unavailable: %v", c.kubeErr), nil
	}
	if check.Namespace == "" || check.ServiceName == "" {
		return status.Unknown, "service endpoint check requires namespace and serviceName", nil
	}

	selector := labels.Set(map[string]string{
		discoveryv1.LabelServiceName: check.ServiceName,
	}).String()
	slices, err := c.kube.DiscoveryV1().EndpointSlices(check.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return status.Unknown, fmt.Sprintf("failed to read endpoint slices: %v", err), map[string]string{
			"namespace": check.Namespace,
			"service":   check.ServiceName,
		}
	}

	readyAddresses := 0
	totalAddresses := 0
	endpointObjects := 0
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			endpointObjects++
			addressCount := len(endpoint.Addresses)
			totalAddresses += addressCount
			if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
				readyAddresses += addressCount
			}
		}
	}

	minReady := check.MinReady
	if minReady <= 0 {
		minReady = 1
	}
	metadata := map[string]string{
		"namespace":       check.Namespace,
		"service":         check.ServiceName,
		"endpointSlices":  strconv.Itoa(len(slices.Items)),
		"endpointObjects": strconv.Itoa(endpointObjects),
		"addresses":       strconv.Itoa(totalAddresses),
		"readyAddresses":  strconv.Itoa(readyAddresses),
		"minReady":        strconv.Itoa(minReady),
	}

	switch {
	case readyAddresses >= minReady:
		return status.Operational, fmt.Sprintf("%d/%d endpoint addresses ready", readyAddresses, totalAddresses), metadata
	case readyAddresses > 0:
		return status.Degraded, fmt.Sprintf("%d/%d endpoint addresses ready", readyAddresses, totalAddresses), metadata
	default:
		return status.Outage, "no ready endpoint addresses", metadata
	}
}

func (c *Collector) checkHTTP(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	if check.URL == "" {
		return status.Unknown, "http check requires url", nil
	}

	timeout := config.DurationOr(check.Timeout, config.DurationOr(c.cfg.Cluster.HTTP.Timeout, 10*time.Second))
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, check.URL, nil)
	if err != nil {
		return status.Unknown, fmt.Sprintf("invalid http request: %v", err), nil
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return status.Outage, fmt.Sprintf("http request failed: %s", safeNetworkError(err)), sanitizedURLMetadata(check.URL)
	}
	defer resp.Body.Close()

	metadata := sanitizedURLMetadata(check.URL)
	metadata["statusCode"] = strconv.Itoa(resp.StatusCode)

	if statusExpected(resp.StatusCode, check.ExpectedStatuses) {
		return status.Operational, fmt.Sprintf("http returned %d", resp.StatusCode), metadata
	}
	return status.Outage, fmt.Sprintf("http returned %d", resp.StatusCode), metadata
}

func statusExpected(code int, expected []int) bool {
	if len(expected) == 0 {
		return code >= 200 && code < 400
	}
	for _, item := range expected {
		if item == code {
			return true
		}
	}
	return false
}

func sanitizedURLMetadata(raw string) map[string]string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return map[string]string{
		"scheme": parsed.Scheme,
		"host":   sanitizedURLHost(parsed),
	}
}

func sanitizedURLHost(parsed *url.URL) string {
	host := parsed.Hostname()
	if host == "" {
		return ""
	}
	if port := parsed.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

func (c *Collector) checkKubernetesReadyz(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	if c.kube == nil {
		return status.Unknown, fmt.Sprintf("kubernetes client unavailable: %v", c.kubeErr), nil
	}

	path := check.Path
	if path == "" {
		path = "/readyz"
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return status.Unknown, fmt.Sprintf("invalid readyz path: %v", err), nil
	}

	request := c.kube.Discovery().RESTClient().Get().AbsPath(parsed.Path)
	for key, values := range parsed.Query() {
		for _, value := range values {
			request.Param(key, value)
		}
	}

	raw, err := request.DoRaw(ctx)
	if err != nil {
		return status.Outage, fmt.Sprintf("readyz failed: %v", err), map[string]string{"path": path}
	}
	if strings.Contains(string(raw), "failed") {
		return status.Degraded, "readyz returned failing checks", map[string]string{"path": path}
	}
	return status.Operational, "readyz passed", map[string]string{"path": path}
}

func (c *Collector) checkPrometheusQuery(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	baseURL := check.URL
	if baseURL == "" {
		baseURL = c.cfg.Cluster.Prometheus.BaseURL
	}
	if baseURL == "" {
		return status.Unknown, "prometheus query requires url or cluster.prometheus.baseURL", nil
	}
	if check.Query == "" {
		return status.Unknown, "prometheus query requires query", nil
	}

	queryURL := prometheusQueryURL(baseURL)
	parsed, err := url.Parse(queryURL)
	if err != nil {
		return status.Unknown, fmt.Sprintf("invalid prometheus url: %v", err), nil
	}
	values := parsed.Query()
	values.Set("query", check.Query)
	parsed.RawQuery = values.Encode()

	timeout := config.DurationOr(check.Timeout, config.DurationOr(c.cfg.Cluster.Prometheus.Timeout, 10*time.Second))
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return status.Unknown, fmt.Sprintf("invalid prometheus request: %v", err), nil
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	host := sanitizedURLHost(parsed)
	if err != nil {
		return status.Unknown, fmt.Sprintf("prometheus query failed: %s", safeNetworkError(err)), map[string]string{"host": host}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return status.Unknown, fmt.Sprintf("prometheus returned %d", resp.StatusCode), map[string]string{"host": host}
	}

	value, err := decodePrometheusValue(resp)
	if err != nil {
		return status.Unknown, err.Error(), map[string]string{"host": host}
	}

	metadata := map[string]string{
		"host":  host,
		"value": strconv.FormatFloat(value, 'f', -1, 64),
	}
	if check.CriticalBelow != nil && value < *check.CriticalBelow {
		addThresholdMetadata(metadata, "below", "critical", *check.CriticalBelow)
		return status.Outage, fmt.Sprintf("metric value %.4g is below critical threshold", value), metadata
	}
	if check.WarningBelow != nil && value < *check.WarningBelow {
		addThresholdMetadata(metadata, "below", "warning", *check.WarningBelow)
		return status.Degraded, fmt.Sprintf("metric value %.4g is below warning threshold", value), metadata
	}
	if check.CriticalAbove != nil && value > *check.CriticalAbove {
		addThresholdMetadata(metadata, "above", "critical", *check.CriticalAbove)
		return status.Outage, fmt.Sprintf("metric value %.4g is above critical threshold", value), metadata
	}
	if check.WarningAbove != nil && value > *check.WarningAbove {
		addThresholdMetadata(metadata, "above", "warning", *check.WarningAbove)
		return status.Degraded, fmt.Sprintf("metric value %.4g is above warning threshold", value), metadata
	}
	if check.CriticalBelow == nil && check.WarningBelow == nil && check.CriticalAbove == nil && check.WarningAbove == nil && value <= 0 {
		addThresholdMetadata(metadata, "below_or_equal", "critical", 0)
		return status.Outage, "metric value is not positive", metadata
	}
	return status.Operational, fmt.Sprintf("metric value %.4g", value), metadata
}

func addThresholdMetadata(metadata map[string]string, direction, severity string, threshold float64) {
	metadata["thresholdDirection"] = direction
	metadata["thresholdSeverity"] = severity
	metadata["threshold"] = strconv.FormatFloat(threshold, 'f', -1, 64)
	metadata["sampleType"] = "instant"
}

func prometheusQueryURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/api/v1/query") {
		return base
	}
	return base + "/api/v1/query"
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Value []any `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func decodePrometheusValue(resp *http.Response) (float64, error) {
	var decoded prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if decoded.Status != "success" {
		return 0, errors.New("prometheus response status is not success")
	}
	if len(decoded.Data.Result) == 0 || len(decoded.Data.Result[0].Value) < 2 {
		return 0, errors.New("prometheus response has no value")
	}
	raw := decoded.Data.Result[0].Value[1]
	switch value := raw.(type) {
	case string:
		return strconv.ParseFloat(value, 64)
	case float64:
		return value, nil
	default:
		return 0, fmt.Errorf("unexpected prometheus value type %T", raw)
	}
}

func safeNetworkError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func (c *Collector) checkRecentWarnings(ctx context.Context, check config.CheckConfig) (status.Level, string, map[string]string) {
	if c.kube == nil {
		return status.Unknown, fmt.Sprintf("kubernetes client unavailable: %v", c.kubeErr), nil
	}

	since := config.DurationOr(check.Since, 15*time.Minute)
	cutoff := time.Now().Add(-since)
	events, err := c.kube.CoreV1().Events(check.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return status.Unknown, fmt.Sprintf("failed to read events: %v", err), map[string]string{"namespace": check.Namespace}
	}

	warnings := 0
	ignoredWarnings := 0
	ignoredWarningReasons := map[string]int{}
	warningSamples := make([]string, 0, maxWarningSamples)
	for _, event := range events.Items {
		if event.Type != corev1.EventTypeWarning {
			continue
		}
		eventTime := event.LastTimestamp.Time
		if eventTime.IsZero() {
			eventTime = event.EventTime.Time
		}
		if eventTime.IsZero() || !eventTime.After(cutoff) {
			continue
		}
		if reason, ignored := c.ignoredWarningReason(ctx, event); ignored {
			ignoredWarnings++
			ignoredWarningReasons[reason]++
			continue
		}
		warnings++
		if len(warningSamples) < maxWarningSamples {
			warningSamples = append(warningSamples, publicWarningSample(event))
		}
	}

	metadata := map[string]string{
		"namespace":       check.Namespace,
		"warnings":        strconv.Itoa(warnings),
		"ignoredWarnings": strconv.Itoa(ignoredWarnings),
		"since":           since.String(),
	}
	addIgnoredWarningMetadata(metadata, ignoredWarningReasons)
	for index, sample := range warningSamples {
		metadata[fmt.Sprintf("warningSample%d", index+1)] = sample
	}
	if check.CriticalCount > 0 && warnings >= check.CriticalCount {
		return status.Outage, fmt.Sprintf("%d recent warning events", warnings), metadata
	}
	if warnings >= check.WarningCount && check.WarningCount > 0 {
		return status.Degraded, fmt.Sprintf("%d recent warning events", warnings), metadata
	}
	return status.Operational, "no recent warning events above threshold", metadata
}

func (c *Collector) shouldIgnoreWarningEvent(ctx context.Context, event corev1.Event) bool {
	_, ignored := c.ignoredWarningReason(ctx, event)
	return ignored
}

func (c *Collector) ignoredWarningReason(ctx context.Context, event corev1.Event) (string, bool) {
	if isBenignWarningEvent(event) {
		return "benignConflict", true
	}

	if event.InvolvedObject.Kind != "Pod" || event.InvolvedObject.Name == "" {
		return "", false
	}

	namespace := event.InvolvedObject.Namespace
	if namespace == "" {
		namespace = event.Namespace
	}
	if namespace == "" {
		return "", false
	}

	pod, err := c.kube.CoreV1().Pods(namespace).Get(ctx, event.InvolvedObject.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "deletedPod", true
	}
	if err != nil {
		return "", false
	}
	if pod.DeletionTimestamp != nil {
		return "terminatingPod", true
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return "completedPod", true
	case corev1.PodFailed:
		return "failedPod", true
	default:
		return "", false
	}
}

func isBenignWarningEvent(event corev1.Event) bool {
	return strings.Contains(event.Message, "the object has been modified") &&
		strings.Contains(event.Message, "please apply your changes to the latest version")
}

func publicWarningSample(event corev1.Event) string {
	namespace := event.InvolvedObject.Namespace
	if namespace == "" {
		namespace = event.Namespace
	}
	target := strings.TrimSpace(strings.Join([]string{event.InvolvedObject.Kind, event.InvolvedObject.Name}, " "))
	if namespace != "" && target != "" {
		target = namespace + "/" + target
	}

	parts := []string{}
	if target != "" {
		parts = append(parts, target)
	}
	if event.Reason != "" {
		parts = append(parts, event.Reason)
	}
	if message := publicEventMessage(event.Message); message != "" {
		parts = append(parts, message)
	}
	return strings.Join(parts, ": ")
}

func addIgnoredWarningMetadata(metadata map[string]string, reasons map[string]int) {
	for reason, count := range reasons {
		if count <= 0 {
			continue
		}
		metadata["ignored"+capitalizeIdentifier(reason)+"Warnings"] = strconv.Itoa(count)
	}
}

func capitalizeIdentifier(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func publicEventMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	message = publicEventURLPattern.ReplaceAllString(message, "<endpoint>")
	message = publicEventSensitiveKVPattern.ReplaceAllString(message, "$1=<redacted>")
	return truncatePublicValue(message, 180)
}

func truncatePublicValue(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func publicCheckResults(component config.ComponentConfig, componentStatus status.Level, results []status.CheckResult) []status.PublicCheckResult {
	if len(results) == 0 {
		return nil
	}
	public := make([]status.PublicCheckResult, 0, len(results))
	for _, result := range results {
		metadata := publicCheckMetadata(result)
		semantics := publicCheckSemantics(component, componentStatus, result, metadata)
		public = append(public, status.PublicCheckResult{
			ID:            result.ID,
			Name:          result.Name,
			Type:          result.Type,
			Impact:        result.Impact,
			Status:        result.Status,
			Message:       publicCheckMessage(result),
			ReasonCode:    semantics.reasonCode,
			ImpactHint:    semantics.impactHint,
			SignalSummary: semantics.signalSummary,
			Confidence:    semantics.confidence,
			Metadata:      metadata,
		})
	}
	return public
}

type publicSemantics struct {
	reasonCode    string
	impactHint    string
	signalSummary string
	confidence    string
}

func publicCheckSemantics(component config.ComponentConfig, componentStatus status.Level, result status.CheckResult, metadata map[string]string) publicSemantics {
	semantics := publicSemantics{
		signalSummary: signalSummary(result, metadata),
		confidence:    confidenceForCheck(result),
	}
	if result.Status == status.Operational {
		return semantics
	}
	semantics.reasonCode = reasonCodeForCheck(result)
	semantics.impactHint = impactHintForCheck(component, componentStatus, result)
	return semantics
}

func componentStatusForCheck(result status.CheckResult) status.Level {
	if result.Status == status.Operational {
		return status.Operational
	}
	switch result.Impact {
	case config.CheckImpactControlPlane, config.CheckImpactDependency, config.CheckImpactSymptom:
		if result.Status == status.Outage {
			return status.Degraded
		}
		return result.Status
	case config.CheckImpactInformational:
		return status.Operational
	default:
		return result.Status
	}
}

func publicCheckMessage(result status.CheckResult) string {
	message := cleanLine(result.Message)
	if message == "" {
		return ""
	}
	message = publicEventURLPattern.ReplaceAllString(message, "<endpoint>")
	message = publicEventSensitiveKVPattern.ReplaceAllString(message, "$1=<redacted>")
	message = truncatePublicValue(message, 180)
	if isKnownCheckType(result.Type) {
		return message
	}
	if result.Status == status.Operational {
		return "check passed"
	}
	return "check failed"
}

func publicCheckMetadata(result status.CheckResult) map[string]string {
	switch strings.ToLower(result.Type) {
	case "workload":
		return copyMetadataKeys(result.Metadata, "namespace", "kind", "resource", "desired", "ready", "minReady")
	case "serviceendpoints":
		return copyMetadataKeys(result.Metadata, "namespace", "service", "endpointSlices", "endpointObjects", "addresses", "readyAddresses", "minReady")
	case "http":
		return copyMetadataKeys(result.Metadata, "scheme", "host", "statusCode")
	case "kubernetesreadyz":
		return copyMetadataKeys(result.Metadata, "path")
	case "prometheusquery":
		return copyMetadataKeys(result.Metadata, "host", "value", "threshold", "thresholdDirection", "thresholdSeverity", "sampleType")
	case "recentwarnings":
		return copyMetadataKeys(result.Metadata, "namespace", "warnings", "ignoredWarnings", "ignoredBenignConflictWarnings", "ignoredDeletedPodWarnings", "ignoredTerminatingPodWarnings", "ignoredCompletedPodWarnings", "ignoredFailedPodWarnings", "since")
	default:
		return nil
	}
}

func isKnownCheckType(checkType string) bool {
	switch strings.ToLower(checkType) {
	case "workload", "serviceendpoints", "http", "kubernetesreadyz", "prometheusquery", "recentwarnings":
		return true
	default:
		return false
	}
}

func reasonCodeForCheck(result status.CheckResult) string {
	switch strings.ToLower(result.Type) {
	case "workload":
		return "workload_not_ready"
	case "serviceendpoints":
		return "service_endpoints_unready"
	case "http":
		if result.Metadata["statusCode"] != "" {
			return "http_unhealthy"
		}
		return "http_unreachable"
	case "kubernetesreadyz":
		return "kubernetes_readyz_failed"
	case "prometheusquery":
		return "metric_threshold_breached"
	case "recentwarnings":
		code, _, _ := warningClassification(result.Metadata)
		return code
	default:
		return "check_failed"
	}
}

func impactHintForCheck(component config.ComponentConfig, componentStatus status.Level, result status.CheckResult) string {
	if result.Impact == config.CheckImpactInformational || componentStatus == status.Operational {
		return fmt.Sprintf("%s has no confirmed user impact from this signal", component.Name)
	}
	switch result.Impact {
	case config.CheckImpactControlPlane:
		return fmt.Sprintf("%s management operations may be degraded", component.Name)
	case config.CheckImpactDependency:
		return fmt.Sprintf("%s dependencies may be degraded", component.Name)
	case config.CheckImpactSymptom:
		return fmt.Sprintf("%s may be degraded if this symptom persists", component.Name)
	}
	return impactHintForComponent(component, componentStatus)
}

func impactHintForComponent(component config.ComponentConfig, componentStatus status.Level) string {
	if componentStatus == status.Unknown {
		return fmt.Sprintf("%s health is being investigated", component.Name)
	}
	if !component.UserFacing && strings.Contains(strings.ToLower(component.Name), "platform") {
		return "platform operations may be degraded; user-facing products can be affected if the signal persists"
	}
	subject := strings.TrimSpace(strings.TrimSuffix(component.Description, "."))
	if subject == "" {
		subject = component.Name
	}
	switch componentStatus {
	case status.Outage:
		return subject + " may be unavailable"
	case status.Degraded:
		return subject + " may be degraded"
	default:
		return subject + " may be affected"
	}
}

func signalSummary(result status.CheckResult, metadata map[string]string) string {
	name := cleanLine(result.Name)
	switch strings.ToLower(result.Type) {
	case "workload":
		if ratio := readyRatio(metadata, "ready", "desired"); ratio != "" {
			return joinWords(name, ratio)
		}
	case "serviceendpoints":
		if ratio := readyRatio(metadata, "readyAddresses", "addresses"); ratio != "" {
			return joinWords(name, ratio)
		}
	case "http":
		if metadata["statusCode"] != "" {
			return joinWords(name, "HTTP "+metadata["statusCode"])
		}
		if result.Status != status.Operational {
			return joinWords(name, "unreachable")
		}
	case "kubernetesreadyz":
		if result.Status == status.Operational {
			return "Kubernetes API readyz passed"
		}
		return "Kubernetes API readyz failed"
	case "prometheusquery":
		return joinWords(name, metricValue(metadata))
	case "recentwarnings":
		return warningSignalSummary(result.Metadata)
	}
	return name
}

func confidenceForCheck(result status.CheckResult) string {
	switch strings.ToLower(result.Type) {
	case "recentwarnings":
		return "symptom"
	case "workload", "serviceendpoints", "http", "kubernetesreadyz", "prometheusquery":
		return "measurement"
	default:
		return "unknown"
	}
}

func readyRatio(metadata map[string]string, readyKey, desiredKey string) string {
	ready := metadata[readyKey]
	desired := metadata[desiredKey]
	if ready == "" || desired == "" {
		return ""
	}
	return ready + "/" + desired + " ready"
}

func metricValue(metadata map[string]string) string {
	if metadata["value"] == "" {
		return ""
	}
	threshold := metadata["threshold"]
	if threshold == "" {
		threshold = metadata["thresholdValue"]
	}
	if threshold != "" {
		severity := metadata["thresholdSeverity"]
		if severity != "" {
			severity += " "
		}
		switch metadata["thresholdDirection"] {
		case "above":
			return "value " + metadata["value"] + " > " + severity + "threshold " + threshold
		case "below":
			return "value " + metadata["value"] + " < " + severity + "threshold " + threshold
		case "below_or_equal":
			return "value " + metadata["value"] + " <= " + severity + "threshold " + threshold
		}
	}
	return "value " + metadata["value"]
}

func warningSignalSummary(metadata map[string]string) string {
	count := warningCount(metadata)
	_, category, product := warningClassification(metadata)
	parts := []string{}
	if product != "" && category != "" {
		parts = append(parts, product+" "+category)
	} else if category != "" {
		parts = append(parts, category)
	}
	if count != "" {
		parts = append(parts, count)
	}
	if ignored := ignoredWarningCount(metadata); ignored != "" {
		parts = append(parts, ignored)
	}
	return strings.Join(parts, "; ")
}

func warningCount(metadata map[string]string) string {
	warnings := metadata["warnings"]
	since := metadata["since"]
	if warnings == "" {
		return ""
	}
	if since == "" {
		return warnings + " warning events"
	}
	return warnings + " warning events in " + since
}

func ignoredWarningCount(metadata map[string]string) string {
	ignored := metadata["ignoredWarnings"]
	if ignored == "" || ignored == "0" {
		return ""
	}
	return ignored + " ignored as historical noise"
}

func warningClassification(metadata map[string]string) (string, string, string) {
	text := strings.ToLower(strings.Join(warningSamples(metadata, maxWarningSamples), " "))
	var code string
	var category string
	switch {
	case strings.Contains(text, "imagepullbackoff"), strings.Contains(text, "errimagepull"), strings.Contains(text, "pull image"), strings.Contains(text, "failed to pull"):
		code = "image_pull_failure"
		category = "image pull failures"
	case strings.Contains(text, "readiness probe failed"), strings.Contains(text, "liveness probe failed"), strings.Contains(text, "unhealthy"):
		code = "probe_failure"
		category = "probe failures"
	case strings.Contains(text, "back-off restarting"), strings.Contains(text, "crashloopbackoff"):
		code = "container_restart"
		category = "container restarts"
	default:
		code = "recent_warning_events"
		category = "recent Kubernetes warning events"
	}
	return code, category, warningProduct(metadata)
}

func warningProduct(metadata map[string]string) string {
	for _, sample := range warningSamples(metadata, maxWarningSamples) {
		namespace := warningSampleNamespace(sample)
		if product := productFromNamespace(namespace); product != "" {
			return product
		}
	}
	return ""
}

func warningSampleNamespace(sample string) string {
	first := strings.SplitN(sample, "/", 2)[0]
	first = strings.TrimSpace(first)
	if strings.Contains(first, " ") || strings.Contains(first, ":") {
		return ""
	}
	return first
}

func productFromNamespace(namespace string) string {
	normalized := strings.TrimSuffix(namespace, "-frontend")
	normalized = strings.TrimSuffix(normalized, "-system")
	switch normalized {
	case "objectstorage":
		return "Object Storage"
	case "dbprovider", "kb":
		return "Database"
	case "applaunchpad", "app":
		return "App Launchpad"
	case "devbox":
		return "DevBox"
	case "costcenter", "account":
		return "Cost Center"
	case "template":
		return "App Store"
	case "terminal":
		return "Terminal"
	case "cronjob":
		return "CronJob"
	case "kite":
		return "Kite"
	case "sealos":
		return "Console"
	default:
		return ""
	}
}

func warningSamples(metadata map[string]string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	samples := []string{}
	for i := 1; i <= maxWarningSamples; i++ {
		key := fmt.Sprintf("warningSample%d", i)
		if metadata[key] != "" {
			samples = append(samples, cleanLine(metadata[key]))
		}
		if len(samples) >= limit {
			break
		}
	}
	return samples
}

func joinWords(parts ...string) string {
	compact := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			compact = append(compact, strings.TrimSpace(part))
		}
	}
	return strings.Join(compact, " ")
}

func cleanLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func copyMetadataKeys(metadata map[string]string, keys ...string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	copied := map[string]string{}
	for _, key := range keys {
		if value, ok := metadata[key]; ok && value != "" {
			copied[key] = value
		}
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func summarize(level status.Level, checks []status.CheckResult) string {
	if len(checks) == 0 {
		return "No checks configured"
	}
	switch level {
	case status.Operational:
		if hasNonOperationalCheck(checks) {
			return "No user-facing impact detected"
		}
		return "All checks passed"
	case status.Unknown:
		return "Some checks could not be evaluated"
	case status.Degraded:
		return "One or more checks are degraded"
	case status.Outage:
		return "One or more checks are failing"
	default:
		return "Status is unknown"
	}
}

func hasNonOperationalCheck(checks []status.CheckResult) bool {
	for _, check := range checks {
		if check.Status != status.Operational {
			return true
		}
	}
	return false
}

func buildKubeConfig(cfg *config.Config) (*rest.Config, error) {
	kubeconfigPath := cfg.Cluster.Kubeconfig
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("KUBECONFIG")
	}
	if kubeconfigPath != "" {
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
			&clientcmd.ConfigOverrides{CurrentContext: cfg.Cluster.Context},
		).ClientConfig()
	}

	if inClusterConfig, err := rest.InClusterConfig(); err == nil {
		return inClusterConfig, nil
	}

	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".kube", "config")
		if _, statErr := os.Stat(path); statErr == nil {
			return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				&clientcmd.ClientConfigLoadingRules{ExplicitPath: path},
				&clientcmd.ConfigOverrides{CurrentContext: cfg.Cluster.Context},
			).ClientConfig()
		}
	}

	return nil, errors.New("no kubeconfig or in-cluster config available")
}
