package openstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlhiter/sealos-collector/internal/status"
)

const defaultReportTitlePrefix = "Sealos Collector:"
const defaultSnapshotMaxAge = 5 * time.Minute

const (
	statusPipelineComponentID = "status-pipeline"
	statusPipelineCheckID     = "snapshot-freshness"
	statusPipelineGroup       = "Status"
)

var (
	publicLineURLPattern         = regexp.MustCompile(`https?://[^\s"]+`)
	publicLineSensitiveKVPattern = regexp.MustCompile(`(?i)(token|password|secret|key)=([^&\s]+)`)
)

type Options struct {
	DatabaseURL       string
	WorkspaceSlug     string
	WorkspaceName     string
	PageSlug          string
	PageTitle         string
	PageDescription   string
	IncludeInternal   bool
	ShowUptime        bool
	SnapshotMaxAge    time.Duration
	Now               func() time.Time
	ReportTitlePrefix string
}

func (o *Options) ApplyDefaults(snapshot *status.Snapshot) {
	if o.WorkspaceSlug == "" {
		o.WorkspaceSlug = "sealos"
	}
	if o.WorkspaceName == "" {
		o.WorkspaceName = "Sealos"
	}
	if o.PageSlug == "" && snapshot != nil && snapshot.Cluster.ID != "" {
		o.PageSlug = "sealos-" + strings.TrimPrefix(snapshot.Cluster.ID, "dev-")
	}
	if o.PageSlug == "" {
		o.PageSlug = "sealos-status"
	}
	if o.PageTitle == "" && snapshot != nil && snapshot.Cluster.Name != "" {
		o.PageTitle = snapshot.Cluster.Name
	}
	if o.PageTitle == "" {
		o.PageTitle = "Sealos Status"
	}
	if o.PageDescription == "" {
		o.PageDescription = "Automated Sealos platform health collected from cluster evidence."
	}
	if o.ReportTitlePrefix == "" {
		o.ReportTitlePrefix = defaultReportTitlePrefix
	}
	if o.SnapshotMaxAge <= 0 {
		o.SnapshotMaxAge = defaultSnapshotMaxAge
	}
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.DatabaseURL) == "" {
		return fmt.Errorf("database url is required")
	}
	if strings.TrimSpace(o.WorkspaceSlug) == "" {
		return fmt.Errorf("workspace slug is required")
	}
	if strings.TrimSpace(o.PageSlug) == "" {
		return fmt.Errorf("page slug is required")
	}
	return nil
}

type Syncer struct {
	db      *Client
	options Options
}

type SyncResult struct {
	WorkspaceID     int64
	PageID          int64
	Components      int
	ReportsCreated  int
	ReportsUpdated  int
	ReportsResolved int
	StaleRemoved    int
}

func NewSyncer(options Options) (*Syncer, error) {
	db, err := NewClient(options.DatabaseURL)
	if err != nil {
		return nil, err
	}
	return &Syncer{db: db, options: options}, nil
}

func LoadSnapshot(path string) (*status.Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snapshot status.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Syncer) Sync(ctx context.Context, snapshot *status.Snapshot) (*SyncResult, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot is required")
	}
	options := s.options
	options.ApplyDefaults(snapshot)
	if err := options.Validate(); err != nil {
		return nil, err
	}

	workspaceID, err := s.ensureWorkspace(ctx, options.WorkspaceSlug, options.WorkspaceName)
	if err != nil {
		return nil, err
	}
	pageID, err := s.ensurePage(ctx, workspaceID, options)
	if err != nil {
		return nil, err
	}

	result := &SyncResult{
		WorkspaceID: workspaceID,
		PageID:      pageID,
	}

	groupIDs := map[string]int64{}
	groupOrder := map[string]int{}
	components := filterComponents(snapshot.Components, options.IncludeInternal)
	if freshness := statusPipelineComponent(snapshot, options.now(), options.SnapshotMaxAge); freshness.ID != "" {
		components = append(components, freshness)
	}
	sort.SliceStable(components, func(i, j int) bool {
		if components[i].Group == components[j].Group {
			return components[i].Name < components[j].Name
		}
		return components[i].Group < components[j].Group
	})

	desiredComponentIDs := make([]int64, 0, len(components))
	for index, component := range components {
		var groupID *int64
		if component.Group != "" {
			id, ok := groupIDs[component.Group]
			if !ok {
				id, err = s.ensureGroup(ctx, workspaceID, pageID, component.Group)
				if err != nil {
					return nil, err
				}
				groupIDs[component.Group] = id
			}
			groupOrder[component.Group]++
			groupID = &id
		}

		componentID, err := s.ensurePageComponent(ctx, workspaceID, pageID, component, index, groupID, groupOrder[component.Group], options.ShowUptime)
		if err != nil {
			return nil, err
		}
		desiredComponentIDs = append(desiredComponentIDs, componentID)
		result.Components++

		action, err := s.syncComponentReport(ctx, pageID, componentID, component, snapshot.GeneratedAt, options.ReportTitlePrefix)
		if err != nil {
			return nil, err
		}
		switch action {
		case reportCreated:
			result.ReportsCreated++
		case reportUpdated:
			result.ReportsUpdated++
		case reportResolved:
			result.ReportsResolved++
		}
	}
	staleReportsResolved, err := s.resolveUnmanagedCollectorReports(ctx, pageID, desiredComponentIDs, options.ReportTitlePrefix, snapshot.GeneratedAt)
	if err != nil {
		return nil, err
	}
	result.ReportsResolved += staleReportsResolved

	staleRemoved, err := s.cleanupStaleComponents(ctx, pageID, desiredComponentIDs)
	if err != nil {
		return nil, err
	}
	result.StaleRemoved = staleRemoved
	if !options.ShowUptime {
		if err := s.disableCollectorMonitors(ctx, workspaceID); err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func filterComponents(components []status.Component, includeInternal bool) []status.Component {
	filtered := make([]status.Component, 0, len(components))
	for _, component := range components {
		if includeInternal || component.UserFacing {
			filtered = append(filtered, component)
		}
	}
	return filtered
}

type snapshotFreshness struct {
	enabled     bool
	stale       bool
	generatedAt time.Time
	age         time.Duration
	maxAge      time.Duration
}

func evaluateSnapshotFreshness(snapshot *status.Snapshot, now time.Time, fallbackMaxAge time.Duration) snapshotFreshness {
	if fallbackMaxAge <= 0 {
		fallbackMaxAge = defaultSnapshotMaxAge
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	evaluation := snapshotFreshness{
		enabled:     true,
		generatedAt: snapshot.GeneratedAt,
		maxAge:      snapshot.MaxAge(fallbackMaxAge),
	}
	if evaluation.maxAge <= 0 {
		evaluation.enabled = false
		return evaluation
	}
	if evaluation.generatedAt.IsZero() {
		evaluation.stale = true
		return evaluation
	}
	evaluation.age = now.Sub(evaluation.generatedAt)
	if evaluation.age < 0 {
		evaluation.age = 0
	}
	evaluation.stale = evaluation.age > evaluation.maxAge
	return evaluation
}

func statusPipelineComponent(snapshot *status.Snapshot, now time.Time, fallbackMaxAge time.Duration) status.Component {
	evaluation := evaluateSnapshotFreshness(snapshot, now, fallbackMaxAge)
	if !evaluation.enabled {
		return status.Component{}
	}

	ageSeconds := durationSeconds(evaluation.age)
	maxAgeSeconds := durationSeconds(evaluation.maxAge)
	generatedAt := ""
	if !evaluation.generatedAt.IsZero() {
		generatedAt = evaluation.generatedAt.UTC().Format(time.RFC3339)
	}

	level := status.Operational
	summary := "Status data is fresh"
	message := "status data is fresh"
	signal := fmt.Sprintf("snapshot age %ds within max age %ds", ageSeconds, maxAgeSeconds)
	reasonCode := ""
	impactHint := ""
	if evaluation.stale {
		level = status.Degraded
		summary = "Status data is stale"
		message = "status data is stale"
		reasonCode = "snapshot_stale"
		impactHint = "status page data may lag behind current platform health"
		if evaluation.generatedAt.IsZero() {
			signal = fmt.Sprintf("snapshot generatedAt is missing; max age %ds", maxAgeSeconds)
		} else {
			signal = fmt.Sprintf("snapshot age %ds exceeds max age %ds", ageSeconds, maxAgeSeconds)
		}
	}

	metadata := map[string]string{
		"ageSeconds":    strconv.FormatInt(ageSeconds, 10),
		"maxAgeSeconds": strconv.FormatInt(maxAgeSeconds, 10),
	}
	if generatedAt != "" {
		metadata["generatedAt"] = generatedAt
	}

	return status.Component{
		ID:          statusPipelineComponentID,
		Name:        "Status Pipeline",
		Group:       statusPipelineGroup,
		Description: "Freshness of collector snapshot data powering this status page.",
		UserFacing:  true,
		Status:      level,
		Summary:     summary,
		PublicChecks: []status.PublicCheckResult{
			{
				ID:            statusPipelineCheckID,
				Name:          "Collector snapshot freshness",
				Type:          "snapshotFreshness",
				Impact:        "symptom",
				Status:        level,
				Message:       message,
				ReasonCode:    reasonCode,
				ImpactHint:    impactHint,
				SignalSummary: signal,
				Confidence:    "measurement",
				Metadata:      metadata,
			},
		},
	}
}

func (s *Syncer) ensureWorkspace(ctx context.Context, slug, name string) (int64, error) {
	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM workspace WHERE lower(slug) = lower(%s) LIMIT 1",
		sqlQuote(slug),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE workspace SET name = %s, updated_at = strftime('%%s','now') WHERE id = %d",
			sqlQuote(name), id,
		))
		return id, err
	}
	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO workspace (slug, name, limits, created_at, updated_at) VALUES (%s, %s, '{}', strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		sqlQuote(slug), sqlQuote(name),
	))
}

func (s *Syncer) ensurePage(ctx context.Context, workspaceID int64, options Options) (int64, error) {
	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM page WHERE lower(slug) = lower(%s) LIMIT 1",
		sqlQuote(options.PageSlug),
	))
	if err != nil {
		return 0, err
	}

	configuration, err := pageConfiguration(options.ShowUptime)
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page SET workspace_id = %d, title = %s, description = %s, custom_domain = '', published = 1, access_type = 'public', default_locale = 'en', legacy_page = 0, allow_index = 1, configuration = %s, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, sqlQuote(options.PageTitle), sqlQuote(options.PageDescription), sqlQuote(configuration), id,
		))
		return id, err
	}

	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO page (workspace_id, title, description, icon, slug, custom_domain, published, access_type, default_locale, legacy_page, allow_index, configuration, created_at, updated_at) VALUES (%d, %s, %s, '', %s, '', 1, 'public', 'en', 0, 1, %s, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		workspaceID, sqlQuote(options.PageTitle), sqlQuote(options.PageDescription), sqlQuote(options.PageSlug), sqlQuote(configuration),
	))
}

func pageConfiguration(showUptime bool) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"type":   "manual",
		"value":  "manual",
		"uptime": showUptime,
		"theme":  "default-rounded",
		"days":   45,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Syncer) ensureGroup(ctx context.Context, workspaceID, pageID int64, name string) (int64, error) {
	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM page_component_groups WHERE page_id = %d AND name = %s LIMIT 1",
		pageID, sqlQuote(name),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page_component_groups SET workspace_id = %d, default_open = 1, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, id,
		))
		return id, err
	}
	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO page_component_groups (workspace_id, page_id, name, default_open, created_at, updated_at) VALUES (%d, %d, %s, 1, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		workspaceID, pageID, sqlQuote(name),
	))
}

func (s *Syncer) ensureMonitor(ctx context.Context, workspaceID int64, component status.Component) (int64, error) {
	key := componentKey(component)
	monitorName := collectorMonitorName(component)
	monitorURL := "https://sealos-collector.invalid/" + key

	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM monitor WHERE workspace_id = %d AND name = %s AND deleted_at IS NULL LIMIT 1",
		workspaceID, sqlQuote(monitorName),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE monitor SET external_name = %s, description = %s, url = %s, active = 1, public = 1, status = 'active', periodicity = 'other', regions = '', updated_at = strftime('%%s','now') WHERE id = %d",
			sqlQuote(component.Name), sqlQuote(component.Description), sqlQuote(monitorURL), id,
		))
		return id, err
	}

	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO monitor (job_type, periodicity, status, active, regions, url, name, external_name, description, headers, body, method, workspace_id, public, retry, follow_redirects, created_at, updated_at) VALUES ('http', 'other', 'active', 1, '', %s, %s, %s, %s, '', '', 'GET', %d, 1, 0, 1, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		sqlQuote(monitorURL), sqlQuote(monitorName), sqlQuote(component.Name), sqlQuote(component.Description), workspaceID,
	))
}

func (s *Syncer) ensurePageComponent(ctx context.Context, workspaceID, pageID int64, component status.Component, order int, groupID *int64, groupOrder int, showUptime bool) (int64, error) {
	if !showUptime {
		return s.ensureStaticComponent(ctx, workspaceID, pageID, component, order, groupID, groupOrder)
	}
	monitorID, err := s.ensureMonitor(ctx, workspaceID, component)
	if err != nil {
		return 0, err
	}
	return s.ensureMonitorComponent(ctx, workspaceID, pageID, monitorID, component, order, groupID, groupOrder)
}

func (s *Syncer) ensureMonitorComponent(ctx context.Context, workspaceID, pageID, monitorID int64, component status.Component, order int, groupID *int64, groupOrder int) (int64, error) {
	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM page_component WHERE page_id = %d AND type = 'monitor' AND monitor_id = %d ORDER BY id LIMIT 1",
		pageID, monitorID,
	))
	if err != nil {
		return 0, err
	}

	groupValue := "NULL"
	if groupID != nil {
		groupValue = fmt.Sprintf("%d", *groupID)
	}

	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page_component SET workspace_id = %d, name = %s, description = %s, \"order\" = %d, group_id = %s, group_order = %d, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder, id,
		))
		return id, err
	}

	id, ok, err = s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM page_component WHERE page_id = %d AND type = 'static' AND name = %s ORDER BY id LIMIT 1",
		pageID, sqlQuote(component.Name),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page_component SET workspace_id = %d, type = 'monitor', monitor_id = %d, name = %s, description = %s, \"order\" = %d, group_id = %s, group_order = %d, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, monitorID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder, id,
		))
		return id, err
	}

	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO page_component (workspace_id, page_id, type, monitor_id, name, description, \"order\", group_id, group_order, created_at, updated_at) VALUES (%d, %d, 'monitor', %d, %s, %s, %d, %s, %d, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		workspaceID, pageID, monitorID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder,
	))
}

func (s *Syncer) ensureStaticComponent(ctx context.Context, workspaceID, pageID int64, component status.Component, order int, groupID *int64, groupOrder int) (int64, error) {
	groupValue := "NULL"
	if groupID != nil {
		groupValue = fmt.Sprintf("%d", *groupID)
	}

	id, ok, err := s.queryID(ctx, fmt.Sprintf(
		"SELECT pc.id FROM page_component pc JOIN monitor m ON m.id = pc.monitor_id WHERE pc.page_id = %d AND pc.type = 'monitor' AND m.workspace_id = %d AND m.name = %s AND m.deleted_at IS NULL ORDER BY pc.id LIMIT 1",
		pageID, workspaceID, sqlQuote(collectorMonitorName(component)),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page_component SET workspace_id = %d, type = 'static', monitor_id = NULL, name = %s, description = %s, \"order\" = %d, group_id = %s, group_order = %d, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder, id,
		))
		return id, err
	}

	id, ok, err = s.queryID(ctx, fmt.Sprintf(
		"SELECT id FROM page_component WHERE page_id = %d AND type = 'static' AND name = %s ORDER BY id LIMIT 1",
		pageID, sqlQuote(component.Name),
	))
	if err != nil {
		return 0, err
	}
	if ok {
		_, err = s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE page_component SET workspace_id = %d, monitor_id = NULL, name = %s, description = %s, \"order\" = %d, group_id = %s, group_order = %d, updated_at = strftime('%%s','now') WHERE id = %d",
			workspaceID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder, id,
		))
		return id, err
	}

	return s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO page_component (workspace_id, page_id, type, monitor_id, name, description, \"order\", group_id, group_order, created_at, updated_at) VALUES (%d, %d, 'static', NULL, %s, %s, %d, %s, %d, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		workspaceID, pageID, sqlQuote(component.Name), sqlQuote(component.Description), order, groupValue, groupOrder,
	))
}

func (s *Syncer) cleanupStaleComponents(ctx context.Context, pageID int64, desiredComponentIDs []int64) (int, error) {
	idList := int64List(desiredComponentIDs)
	if idList == "" {
		idList = "0"
	}
	staleSelector := fmt.Sprintf(
		"SELECT pc.id FROM page_component pc JOIN monitor m ON m.id = pc.monitor_id WHERE pc.page_id = %d AND pc.type = 'monitor' AND m.name LIKE 'sealos-collector:%%' AND pc.id NOT IN (%s)",
		pageID, idList,
	)
	rows, err := s.db.Execute(ctx, staleSelector)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) == 0 {
			continue
		}
		id, err := parseInt64(row[0].Value)
		if err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	staleIDs := int64List(ids)
	if _, err := s.db.Execute(ctx, fmt.Sprintf("DELETE FROM status_report_to_page_component WHERE page_component_id IN (%s)", staleIDs)); err != nil {
		return 0, err
	}
	if _, err := s.db.Execute(ctx, fmt.Sprintf("DELETE FROM status_report_update_to_page_component WHERE page_component_id IN (%s)", staleIDs)); err != nil {
		return 0, err
	}
	if _, err := s.db.Execute(ctx, fmt.Sprintf("DELETE FROM page_component WHERE id IN (%s)", staleIDs)); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Syncer) resolveUnmanagedCollectorReports(ctx context.Context, pageID int64, desiredComponentIDs []int64, titlePrefix string, observedAt time.Time) (int, error) {
	idList := int64List(desiredComponentIDs)
	if idList == "" {
		idList = "0"
	}
	rows, err := s.db.Execute(ctx, fmt.Sprintf(
		"SELECT sr.id FROM status_report sr LEFT JOIN status_report_to_page_component pc ON pc.status_report_id = sr.id WHERE sr.page_id = %d AND sr.status <> 'resolved' AND sr.title LIKE %s GROUP BY sr.id HAVING SUM(CASE WHEN pc.page_component_id IN (%s) THEN 1 ELSE 0 END) = 0",
		pageID, sqlQuote(strings.TrimSpace(titlePrefix)+"%"), idList,
	))
	if err != nil {
		return 0, err
	}

	ids := make([]int64, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) == 0 {
			continue
		}
		id, err := parseInt64(row[0].Value)
		if err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		if _, err := s.db.Execute(ctx, fmt.Sprintf(
			"INSERT INTO status_report_update (status, date, message, status_report_id, created_at, updated_at) VALUES ('resolved', %d, %s, %d, strftime('%%s','now'), strftime('%%s','now'))",
			unixSeconds(observedAt), sqlQuote("Collector no longer manages this component; marking stale report resolved."), id,
		)); err != nil {
			return 0, err
		}
		if _, err := s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE status_report SET status = 'resolved', updated_at = %d WHERE id = %d",
			unixSeconds(observedAt), id,
		)); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func (s *Syncer) disableCollectorMonitors(ctx context.Context, workspaceID int64) error {
	_, err := s.db.Execute(ctx, fmt.Sprintf(
		"UPDATE monitor SET active = 0, public = 0, updated_at = strftime('%%s','now') WHERE workspace_id = %d AND name LIKE 'sealos-collector:%%' AND deleted_at IS NULL",
		workspaceID,
	))
	return err
}

type reportAction string

const (
	reportNoop     reportAction = "noop"
	reportCreated  reportAction = "created"
	reportUpdated  reportAction = "updated"
	reportResolved reportAction = "resolved"
)

func (s *Syncer) syncComponentReport(ctx context.Context, pageID, componentID int64, component status.Component, observedAt time.Time, titlePrefix string) (reportAction, error) {
	reportID, active, err := s.activeReport(ctx, pageID, componentID, titlePrefix)
	if err != nil {
		return reportNoop, err
	}

	if component.Status == status.Operational {
		if !active {
			return reportNoop, nil
		}
		message := reportUpdateMessage(component, true)
		if err := s.addReportUpdate(ctx, reportID, componentID, "resolved", message, "operational", observedAt); err != nil {
			return reportNoop, err
		}
		_, err := s.db.Execute(ctx, fmt.Sprintf(
			"UPDATE status_report SET status = 'resolved', updated_at = %d WHERE id = %d",
			unixSeconds(observedAt), reportID,
		))
		if err != nil {
			return reportNoop, err
		}
		return reportResolved, nil
	}

	reportStatus := reportStatusForLevel(component.Status)
	impact := impactForLevel(component.Status)
	title := fmt.Sprintf("%s %s %s", strings.TrimSpace(titlePrefix), component.Name, component.Status)
	message := reportUpdateMessage(component, false)

	if !active {
		reportID, err = s.insertID(ctx, fmt.Sprintf(
			"INSERT INTO status_report (status, title, workspace_id, page_id, created_at, updated_at) SELECT %s, %s, workspace_id, id, %d, %d FROM page WHERE id = %d RETURNING id",
			sqlQuote(reportStatus), sqlQuote(title), unixSeconds(observedAt), unixSeconds(observedAt), pageID,
		))
		if err != nil {
			return reportNoop, err
		}
		if err := s.linkReportComponent(ctx, reportID, componentID); err != nil {
			return reportNoop, err
		}
		if err := s.addReportUpdate(ctx, reportID, componentID, reportStatus, message, impact, observedAt); err != nil {
			return reportNoop, err
		}
		return reportCreated, nil
	}

	_, err = s.db.Execute(ctx, fmt.Sprintf(
		"UPDATE status_report SET status = %s, title = %s, updated_at = %d WHERE id = %d",
		sqlQuote(reportStatus), sqlQuote(title), unixSeconds(observedAt), reportID,
	))
	if err != nil {
		return reportNoop, err
	}
	if err := s.linkReportComponent(ctx, reportID, componentID); err != nil {
		return reportNoop, err
	}

	latest, ok, err := s.latestUpdate(ctx, reportID, componentID)
	if err != nil {
		return reportNoop, err
	}
	if ok && latest.Status == reportStatus && latest.Message == message && latest.Impact == impact {
		return reportNoop, nil
	}

	if err := s.addReportUpdate(ctx, reportID, componentID, reportStatus, message, impact, observedAt); err != nil {
		return reportNoop, err
	}
	return reportUpdated, nil
}

func reportUpdateMessage(component status.Component, recovered bool) string {
	if recovered {
		return fmt.Sprintf("%s recovered: %s.", component.Name, recoveredSummary(component))
	}

	checks := reportChecks(component, false)
	lines := []string{
		fmt.Sprintf("%s %s: %s.", component.Name, component.Status, trimSentence(incidentCause(component, checks))),
	}
	if impact := incidentImpact(component, checks); impact != "" {
		lines = append(lines, "Impact: "+ensureSentence(impact))
	}
	if signal := incidentSignal(checks); signal != "" {
		lines = append(lines, "Signal: "+ensureSentence(signal))
	}
	return strings.Join(lines, "\n")
}

func recoveredSummary(component status.Component) string {
	checks := reportChecks(component, true)
	if len(checks) == 0 {
		return component.Summary
	}
	if len(checks) == 1 {
		if checks[0].SignalSummary != "" {
			return cleanLine(checks[0].SignalSummary)
		}
		if checks[0].Message != "" {
			return cleanLine(checks[0].Message)
		}
		return joinWords(cleanLine(checks[0].Name), "operational")
	}
	return fmt.Sprintf("%d checks operational", len(checks))
}

func reportChecks(component status.Component, recovered bool) []status.PublicCheckResult {
	checks := component.PublicChecks
	if len(checks) == 0 {
		checks = publicChecksFromDetailedChecks(component.Checks)
	}
	filtered := make([]status.PublicCheckResult, 0, len(checks))
	for _, check := range checks {
		if recovered {
			if check.Status == status.Operational {
				filtered = append(filtered, check)
			}
			continue
		}
		if check.Status != status.Operational {
			filtered = append(filtered, check)
		}
	}
	if len(filtered) > 5 {
		return filtered[:5]
	}
	return filtered
}

func publicChecksFromDetailedChecks(checks []status.CheckResult) []status.PublicCheckResult {
	public := make([]status.PublicCheckResult, 0, len(checks))
	for _, check := range checks {
		public = append(public, status.PublicCheckResult{
			ID:      check.ID,
			Name:    check.Name,
			Type:    check.Type,
			Status:  check.Status,
			Message: publicDetailedCheckMessage(check),
		})
	}
	return public
}

func publicDetailedCheckMessage(check status.CheckResult) string {
	if check.Message == "" {
		return ""
	}
	if !isKnownPublicCheckType(check.Type) {
		if check.Status == status.Operational {
			return "check passed"
		}
		return "check failed"
	}
	return sanitizePublicLine(check.Message)
}

func isKnownPublicCheckType(checkType string) bool {
	switch strings.ToLower(checkType) {
	case "workload", "serviceendpoints", "http", "kubernetesreadyz", "prometheusquery", "recentwarnings":
		return true
	default:
		return false
	}
}

func incidentCause(component status.Component, checks []status.PublicCheckResult) string {
	if len(checks) == 0 {
		if component.Summary != "" {
			return cleanLine(component.Summary)
		}
		return "health signal changed"
	}
	primary := primaryIncidentCheck(checks)
	if cause := structuredCauseForCheck(primary); cause != "" {
		return cause
	}
	return causeForCheck(primary)
}

func primaryIncidentCheck(checks []status.PublicCheckResult) status.PublicCheckResult {
	primary := checks[0]
	for _, check := range checks[1:] {
		if levelRank(check.Status) > levelRank(primary.Status) {
			primary = check
		}
	}
	return primary
}

func structuredCauseForCheck(check status.PublicCheckResult) string {
	if check.ReasonCode == "" {
		return ""
	}
	name := cleanLine(check.Name)
	signal := cleanLine(check.SignalSummary)
	evidence := signalEvidence(signal, name)
	switch check.ReasonCode {
	case "workload_not_ready":
		if evidence != "" {
			return fmt.Sprintf("%s is not ready (%s)", name, evidence)
		}
		return joinWords(name, "is not ready")
	case "service_endpoints_unready":
		if evidence != "" {
			return fmt.Sprintf("%s has insufficient ready endpoints (%s)", name, evidence)
		}
		return joinWords(name, "has insufficient ready endpoints")
	case "http_unhealthy":
		if code := httpStatusCode(check, evidence); code != "" {
			return fmt.Sprintf("%s returned HTTP %s", name, code)
		}
		return joinWords(name, "returned an unhealthy response")
	case "http_unreachable":
		return joinWords(name, "is unreachable")
	case "kubernetes_readyz_failed":
		return "Kubernetes API readiness failed"
	case "metric_threshold_breached":
		return joinWords(name, "breached its health threshold", evidence)
	case "snapshot_stale":
		if evidence != "" {
			return joinWords(name, "is stale", "("+evidence+")")
		}
		return joinWords(name, "is stale")
	case "image_pull_failure", "probe_failure", "container_restart", "recent_warning_events":
		if subject := signalSubject(signal); subject != "" {
			return subject + " detected"
		}
		return reasonText(check.ReasonCode)
	case "check_failed":
		return joinWords(name, "check failed")
	default:
		if signal != "" {
			return signal
		}
		return joinWords(name, "check failed")
	}
}

func signalEvidence(signal, name string) string {
	if signal == "" {
		return ""
	}
	if name != "" && strings.HasPrefix(signal, name+" ") {
		return strings.TrimSpace(strings.TrimPrefix(signal, name+" "))
	}
	return signal
}

func signalSubject(signal string) string {
	subject := strings.TrimSpace(strings.SplitN(signal, ";", 2)[0])
	return subject
}

func httpStatusCode(check status.PublicCheckResult, evidence string) string {
	if check.Metadata["statusCode"] != "" {
		return check.Metadata["statusCode"]
	}
	return strings.TrimSpace(strings.TrimPrefix(evidence, "HTTP "))
}

func reasonText(reasonCode string) string {
	switch reasonCode {
	case "image_pull_failure":
		return "image pull failures detected"
	case "probe_failure":
		return "probe failures detected"
	case "container_restart":
		return "container restarts detected"
	case "recent_warning_events":
		return "recent Kubernetes warning events crossed the threshold"
	default:
		return "check failed"
	}
}

func levelRank(level status.Level) int {
	switch level {
	case status.Outage:
		return 3
	case status.Degraded:
		return 2
	case status.Unknown:
		return 1
	default:
		return 0
	}
}

func causeForCheck(check status.PublicCheckResult) string {
	name := cleanLine(check.Name)
	switch strings.ToLower(check.Type) {
	case "workload":
		if ratio := readyRatio(check.Metadata, "ready", "desired"); ratio != "" {
			return fmt.Sprintf("%s is not ready (%s)", name, ratio)
		}
		return joinWords(name, "is not ready")
	case "serviceendpoints":
		if ratio := readyRatio(check.Metadata, "readyAddresses", "addresses"); ratio != "" {
			return fmt.Sprintf("%s has insufficient ready endpoints (%s)", name, ratio)
		}
		return joinWords(name, "has insufficient ready endpoints")
	case "http":
		if check.Metadata["statusCode"] != "" {
			return fmt.Sprintf("%s returned HTTP %s", name, check.Metadata["statusCode"])
		}
		return joinWords(name, "is unreachable")
	case "kubernetesreadyz":
		return "Kubernetes API readiness failed"
	case "prometheusquery":
		return joinWords(name, "breached its health threshold", metricValue(check.Metadata))
	case "recentwarnings":
		if count := warningCount(check.Metadata); count != "" {
			return "recent Kubernetes warning events crossed the threshold (" + count + ")"
		}
		return "recent Kubernetes warning events crossed the threshold"
	case "snapshotfreshness":
		return joinWords(name, "is stale")
	default:
		return joinWords(name, "check failed")
	}
}

func incidentImpact(component status.Component, checks []status.PublicCheckResult) string {
	if len(checks) > 0 {
		primary := primaryIncidentCheck(checks)
		if primary.ImpactHint != "" {
			return cleanLine(primary.ImpactHint)
		}
		for _, check := range checks {
			if check.ImpactHint != "" {
				return cleanLine(check.ImpactHint)
			}
		}
	}
	if component.Status == status.Unknown {
		return fmt.Sprintf("%s health is being investigated", component.Name)
	}
	if !component.UserFacing && strings.Contains(strings.ToLower(component.Name), "platform") {
		return "platform operations may be degraded; user-facing products can be affected if the signal persists"
	}
	subject := strings.TrimSpace(strings.TrimSuffix(component.Description, "."))
	if subject == "" {
		subject = component.Name
	}
	switch component.Status {
	case status.Outage:
		return subject + " may be unavailable"
	case status.Degraded:
		return subject + " may be degraded"
	default:
		return subject + " may be affected"
	}
}

func incidentSignal(checks []status.PublicCheckResult) string {
	signals := make([]string, 0, 2)
	for _, check := range checks {
		signal := cleanLine(check.SignalSummary)
		if signal == "" {
			signal = signalForCheck(check)
		}
		if signal != "" {
			signals = append(signals, signal)
		}
		if len(signals) == 2 {
			break
		}
	}
	if len(signals) == 0 {
		return ""
	}
	if extra := len(checks) - len(signals); extra > 0 {
		signals = append(signals, fmt.Sprintf("+%d more signal", extra))
	}
	return strings.Join(signals, "; ")
}

func signalForCheck(check status.PublicCheckResult) string {
	name := cleanLine(check.Name)
	switch strings.ToLower(check.Type) {
	case "workload":
		if ratio := readyRatio(check.Metadata, "ready", "desired"); ratio != "" {
			return fmt.Sprintf("%s %s", name, ratio)
		}
	case "serviceendpoints":
		if ratio := readyRatio(check.Metadata, "readyAddresses", "addresses"); ratio != "" {
			return fmt.Sprintf("%s %s", name, ratio)
		}
	case "http":
		if check.Metadata["statusCode"] != "" {
			return fmt.Sprintf("%s HTTP %s", name, check.Metadata["statusCode"])
		}
		return joinWords(name, "unreachable")
	case "kubernetesreadyz":
		if check.Metadata["path"] != "" {
			return "Kubernetes API readyz failed"
		}
	case "prometheusquery":
		return joinWords(name, metricValue(check.Metadata))
	case "recentwarnings":
		return warningCount(check.Metadata)
	case "snapshotfreshness":
		return joinWords(name, snapshotFreshnessSignal(check.Metadata))
	}
	if check.Message != "" {
		return name
	}
	return name
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

func snapshotFreshnessSignal(metadata map[string]string) string {
	age := metadata["ageSeconds"]
	maxAge := metadata["maxAgeSeconds"]
	if age == "" || maxAge == "" {
		return "snapshot freshness"
	}
	return "snapshot age " + age + "s / max age " + maxAge + "s"
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

func sanitizePublicLine(value string) string {
	value = cleanLine(value)
	value = publicLineURLPattern.ReplaceAllString(value, "<endpoint>")
	value = publicLineSensitiveKVPattern.ReplaceAllString(value, "$1=<redacted>")
	return truncatePublicLine(value, 180)
}

func truncatePublicLine(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func ensureSentence(value string) string {
	value = trimSentence(value)
	if value == "" {
		return ""
	}
	return value + "."
}

func trimSentence(value string) string {
	return strings.TrimRight(cleanLine(value), ".!?")
}

func (s *Syncer) activeReport(ctx context.Context, pageID, componentID int64, titlePrefix string) (int64, bool, error) {
	sql := fmt.Sprintf(
		"SELECT sr.id FROM status_report sr JOIN status_report_to_page_component pc ON pc.status_report_id = sr.id WHERE sr.page_id = %d AND pc.page_component_id = %d AND sr.status <> 'resolved' AND sr.title LIKE %s ORDER BY sr.created_at DESC, sr.id DESC LIMIT 1",
		pageID, componentID, sqlQuote(strings.TrimSpace(titlePrefix)+"%"),
	)
	return s.queryID(ctx, sql)
}

func (s *Syncer) linkReportComponent(ctx context.Context, reportID, componentID int64) error {
	_, err := s.db.Execute(ctx, fmt.Sprintf(
		"INSERT OR IGNORE INTO status_report_to_page_component (status_report_id, page_component_id, created_at) VALUES (%d, %d, strftime('%%s','now'))",
		reportID, componentID,
	))
	return err
}

func (s *Syncer) addReportUpdate(ctx context.Context, reportID, componentID int64, reportStatus, message, impact string, observedAt time.Time) error {
	updateID, err := s.insertID(ctx, fmt.Sprintf(
		"INSERT INTO status_report_update (status, date, message, status_report_id, created_at, updated_at) VALUES (%s, %d, %s, %d, strftime('%%s','now'), strftime('%%s','now')) RETURNING id",
		sqlQuote(reportStatus), unixSeconds(observedAt), sqlQuote(message), reportID,
	))
	if err != nil {
		return err
	}
	_, err = s.db.Execute(ctx, fmt.Sprintf(
		"INSERT OR REPLACE INTO status_report_update_to_page_component (status_report_update_id, page_component_id, impact, created_at) VALUES (%d, %d, %s, strftime('%%s','now'))",
		updateID, componentID, sqlQuote(impact),
	))
	return err
}

type latestUpdate struct {
	Status  string
	Message string
	Impact  string
}

func (s *Syncer) latestUpdate(ctx context.Context, reportID, componentID int64) (latestUpdate, bool, error) {
	rows, err := s.db.Execute(ctx, fmt.Sprintf(
		"SELECT u.status, u.message, COALESCE(pc.impact, '') FROM status_report_update u LEFT JOIN status_report_update_to_page_component pc ON pc.status_report_update_id = u.id AND pc.page_component_id = %d WHERE u.status_report_id = %d ORDER BY u.date DESC, u.id DESC LIMIT 1",
		componentID, reportID,
	))
	if err != nil {
		return latestUpdate{}, false, err
	}
	if len(rows.Rows) == 0 {
		return latestUpdate{}, false, nil
	}
	row := rows.Rows[0]
	value := latestUpdate{}
	if len(row) > 0 {
		value.Status = row[0].Value
	}
	if len(row) > 1 {
		value.Message = row[1].Value
	}
	if len(row) > 2 {
		value.Impact = row[2].Value
	}
	return value, true, nil
}

func (s *Syncer) queryID(ctx context.Context, sql string) (int64, bool, error) {
	rows, err := s.db.Execute(ctx, sql)
	if err != nil {
		return 0, false, err
	}
	return rows.FirstInt64()
}

func (s *Syncer) insertID(ctx context.Context, sql string) (int64, error) {
	id, ok, err := s.queryID(ctx, sql)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("insert did not return id")
	}
	return id, nil
}

func reportStatusForLevel(level status.Level) string {
	if level == status.Unknown {
		return "investigating"
	}
	return "identified"
}

func impactForLevel(level status.Level) string {
	switch level {
	case status.Outage:
		return "major_outage"
	case status.Degraded:
		return "degraded_performance"
	case status.Unknown:
		return "degraded_performance"
	default:
		return "operational"
	}
}

func unixSeconds(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UTC().Unix()
	}
	return t.UTC().Unix()
}

func int64List(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ",")
}

func durationSeconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return int64((value + time.Second - 1) / time.Second)
}

func parseInt64(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}

func componentKey(component status.Component) string {
	if component.ID != "" {
		return component.ID
	}
	return component.Name
}

func collectorMonitorName(component status.Component) string {
	return "sealos-collector:" + componentKey(component)
}

func sqlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
