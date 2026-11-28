package objectstorage

import (
	"path"
	"strconv"
	"strings"
	"time"
)

type MigrationPreview struct {
	Source  string   `json:"source"`
	Target  string   `json:"target"`
	Total   int      `json:"total"`
	Ready   int      `json:"ready"`
	Bytes   int64    `json:"bytes"`
	Routes  []string `json:"routes"`
	Default bool     `json:"default"`
}

// Migrate changes the routing only after all destination receipts are durable.
// Local originals supply the copy, including when the old cloud is unavailable.
func (s *Service) Migrate(source, target string, version int64, apply bool) (MigrationPreview, error) {
	var result MigrationPreview
	if err := s.load(false); err != nil {
		return result, err
	}
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.mu.Lock()
	c, g, ns := cloneRoutes(s.settings), s.generation, s.namespace
	records := make(map[string]Record, len(s.records))
	for id, r := range s.records {
		records[id] = r
	}
	s.mu.Unlock()
	if c.Version != version {
		return result, ErrConflict
	}
	_, sourceOK := profileByID(c, source)
	p, targetOK := profileByID(c, target)
	if !sourceOK || !targetOK || source == target {
		return result, &OperationError{"请选择不同的来源和目标存储"}
	}
	result = MigrationPreview{Source: source, Target: target, Routes: []string{}, Default: c.ActiveProfile == source}
	for _, entry := range UploadEntries() {
		if destination, _ := routeFor(c, entry.ID); destination == source {
			result.Routes = append(result.Routes, entry.Name)
		}
	}
	if !result.Default && len(result.Routes) == 0 {
		return result, &OperationError{"来源存储未被任何上传入口使用，无需迁移"}
	}
	inventory, err := s.storageInventory()
	if err != nil {
		return result, err
	}
	jobs := []Record{}
	newRecords := 0
	for _, o := range inventory {
		destination, _ := routeFor(c, o.UploadSource)
		if o.Trashed || destination != source {
			continue
		}
		result.Total++
		result.Bytes += o.SizeBytes
		id := recordID(target, o.ItemID, o.ObjectKey)
		r, exists := records[id]
		if exists && r.Status == "synced" && r.Identity == profileIdentity(p) && r.SHA256 == o.ChecksumSHA256 && r.Size == o.SizeBytes {
			result.Ready++
			continue
		}
		if !exists {
			newRecords++
		}
		jobs = append(jobs, Record{ID: id, ProfileID: target, Identity: profileIdentity(p), ItemID: o.ItemID, Filename: o.Filename, ObjectKey: o.ObjectKey, RemoteKey: path.Join(p.Prefix, ns, strconv.FormatInt(o.ItemID, 10), o.ObjectKey), SHA256: o.ChecksumSHA256, Size: o.SizeBytes, ContentType: o.MIMEType, Status: "pending", Operation: "upload", Updated: time.Now().UTC()})
	}
	if len(records)+newRecords > maxRecords {
		return result, errRecordLimit
	}
	if !apply {
		return result, nil
	}
	for _, r := range jobs {
		if err := s.commit(g, version, r, false); err != nil {
			return result, err
		}
	}
	if result.Default {
		c.ActiveProfile = target
	}
	for entry, route := range c.UploadRoutes {
		if route.Target == source {
			route.Target = target
			c.UploadRoutes[entry] = route
		}
	}
	_, err = s.Save(c, nil)
	return result, err
}

type RecordPage struct {
	Records []Record `json:"records"`
	Total   int      `json:"total"`
	Page    int      `json:"page"`
	Pages   int      `json:"pages"`
}

func (s *Service) Records(profile, status, query string, page int) (RecordPage, error) {
	result := RecordPage{Records: []Record{}, Page: page}
	if err := s.load(false); err != nil {
		return result, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clamp before multiplying to keep even an extreme requested page bounded.
	limit := min(max(1, page), max(1, (len(s.records)+99)/100)) * 100
	selected := newRecordSelection(limit, true)
	for _, r := range s.records {
		if (profile == "" || r.ProfileID == profile) && (status == "" || r.Status == status) && (query == "" || strings.Contains(strings.ToLower(r.Filename+" "+r.RemoteKey), query)) {
			result.Total++
			selected.add(r)
		}
	}
	result.Pages = max(1, (result.Total+99)/100)
	result.Page = min(max(1, page), result.Pages)
	start := (result.Page - 1) * 100
	for _, position := range selected.sorted()[start:min(start+100, result.Total)] {
		result.Records = append(result.Records, s.records[position.id])
	}
	return result, nil
}
