package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

const (
	revisionDiffMaxSourceBytes = 64 << 10
	revisionDiffMaxUnits       = 280
	revisionDiffMaxUnitRunes   = 180
	revisionDiffContextUnits   = 2
)

type adminRevisionCompareView struct {
	Post           content.Post
	From           adminRevisionSideView
	To             adminRevisionSideView
	Options        []adminRevisionOptionView
	Fields         []adminRevisionFieldView
	Rows           []adminRevisionDiffRowView
	Blocks         adminRevisionBlockDiffView
	ChangedFields  int
	AddedUnits     int
	RemovedUnits   int
	ChangedUnits   int
	FromCharacters int
	ToCharacters   int
	BodyChanged    bool
	DiffTruncated  bool
	EditorURL      string
	PublicURL      string
	IsPublic       bool
}

type adminRevisionSideView struct {
	Value        string
	Version      int64
	Title        string
	Actor        string
	CreatedLabel string
	StatusLabel  string
	Current      bool
}

type adminRevisionOptionView struct {
	Value        string
	Label        string
	SelectedFrom bool
	SelectedTo   bool
	Current      bool
}

type adminRevisionFieldView struct {
	Label   string
	From    string
	To      string
	Changed bool
}

type adminRevisionDiffRowView struct {
	Kind      string
	FromLine  int
	ToLine    int
	FromText  string
	ToText    string
	Collapsed int
}

type revisionCompareReference struct {
	Value     string
	Snapshot  content.Post
	Actor     string
	CreatedAt time.Time
	Current   bool
}

type revisionDiffOperation struct {
	Kind string
	Text string
}

func (s *Server) adminContentRevisionCompare(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	postID, err := positivePathID(request.PathValue("id"))
	if err != nil || request.URL.Path != "/admin/content/"+request.PathValue("id")+"/revisions/compare/" {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	fromID, toValue, err := parseRevisionCompareQuery(request.URL.RawQuery)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "修订比较参数不正确")
		return
	}
	post, err := s.repository.GetByID(request.Context(), postID)
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "内容没有找到")
		return
	}
	if err != nil {
		slog.Error("load revision comparison post", "id", postID, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "内容暂时不可用")
		return
	}
	if !canReadContentObject(principal, post) {
		s.renderError(writer, http.StatusForbidden, "当前账号不能访问其他作者的修订")
		return
	}

	fromRevision, err := s.repository.GetRevision(request.Context(), postID, fromID)
	if err != nil {
		s.handleRevisionCompareLoadError(writer, postID, fromID, err)
		return
	}
	from := revisionCompareReference{
		Value: strconv.FormatInt(fromRevision.ID, 10), Snapshot: fromRevision.Snapshot,
		Actor: fromRevision.Actor, CreatedAt: fromRevision.CreatedAt,
	}
	to := revisionCompareReference{Value: "current", Snapshot: post, Actor: "当前存储版本", CreatedAt: post.UpdatedAt, Current: true}
	if toValue != "current" {
		toID, _ := strconv.ParseInt(toValue, 10, 64)
		toRevision, loadErr := s.repository.GetRevision(request.Context(), postID, toID)
		if loadErr != nil {
			s.handleRevisionCompareLoadError(writer, postID, toID, loadErr)
			return
		}
		to = revisionCompareReference{
			Value: strconv.FormatInt(toRevision.ID, 10), Snapshot: toRevision.Snapshot,
			Actor: toRevision.Actor, CreatedAt: toRevision.CreatedAt,
		}
	}

	limit := s.siteSettings.Snapshot().Settings.Reading.RevisionLimit
	revisions, err := s.repository.ListRevisions(request.Context(), postID, limit)
	if err != nil {
		slog.Error("list revision comparison options", "id", postID, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "修订记录暂时不可用")
		return
	}
	readOnly = readOnly || !canWriteContentObject(principal, post)
	s.renderRevisionComparison(writer, request, principal, readOnly, post, from, to, revisions)
}

func (s *Server) handleRevisionCompareLoadError(writer http.ResponseWriter, postID, revisionID int64, err error) {
	if errors.Is(err, content.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "修订没有找到或不属于这篇内容")
		return
	}
	slog.Error("load revision comparison snapshot", "post_id", postID, "revision_id", revisionID, "error", err)
	s.renderError(writer, http.StatusInternalServerError, "修订记录暂时不可用")
}

func (s *Server) renderRevisionComparison(writer http.ResponseWriter, request *http.Request, principal auth.Principal, readOnly bool, post content.Post, from, to revisionCompareReference, revisions []content.Revision) {
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	fields, changedFields := revisionFieldComparison(from.Snapshot, to.Snapshot, location)
	rows, added, removed, changed, truncated := buildRevisionBodyDiff(from.Snapshot.ContentSource, to.Snapshot.ContentSource)
	blocks := buildRevisionBlockDiff(from.Snapshot.ContentSource, to.Snapshot.ContentSource)
	options := revisionCompareOptions(revisions, from, to, post.Version)
	publicURL := s.contentPath(post)
	data := s.baseViewFor(writer, "修订比较", "在恢复前核对字段、正文与工作流差异", request.URL.Path)
	data.BodyClass = "is-admin is-admin-revision-compare"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("content", principal, readOnly)
	data.Revision = adminRevisionCompareView{
		Post: post,
		From: revisionSideView(from, location), To: revisionSideView(to, location), Options: options,
		Fields: fields, Rows: rows, Blocks: blocks, ChangedFields: changedFields,
		AddedUnits: added, RemovedUnits: removed, ChangedUnits: changed,
		FromCharacters: utf8.RuneCountInString(from.Snapshot.ContentSource),
		ToCharacters:   utf8.RuneCountInString(to.Snapshot.ContentSource),
		BodyChanged:    from.Snapshot.ContentSource != to.Snapshot.ContentSource, DiffTruncated: truncated,
		EditorURL: "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/",
		PublicURL: publicURL, IsPublic: content.PublicAt(post, time.Now()),
	}
	access := func() error { return s.contentObjectAccess(request, post.ID, false, false) }
	s.renderChecked(writer, http.StatusOK, "admin-revision-compare", data, request, s.contentBeforeOutput(writer, request, false, access), access)
}

func parseRevisionCompareQuery(raw string) (int64, string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) != 2 {
		return 0, "", errors.New("invalid revision comparison query")
	}
	for key, items := range values {
		if (key != "from" && key != "to") || len(items) != 1 || strings.TrimSpace(items[0]) != items[0] || items[0] == "" {
			return 0, "", errors.New("invalid revision comparison query shape")
		}
	}
	fromID, err := strconv.ParseInt(values.Get("from"), 10, 64)
	if err != nil || fromID <= 0 {
		return 0, "", errors.New("invalid from revision")
	}
	to := values.Get("to")
	if to != "current" {
		toID, parseErr := strconv.ParseInt(to, 10, 64)
		if parseErr != nil || toID <= 0 {
			return 0, "", errors.New("invalid to revision")
		}
		to = strconv.FormatInt(toID, 10)
	}
	return fromID, to, nil
}

func revisionSideView(reference revisionCompareReference, location *time.Location) adminRevisionSideView {
	actor := strings.TrimSpace(reference.Actor)
	if actor == "" {
		actor = reference.Snapshot.AuthorName
	}
	return adminRevisionSideView{
		Value: reference.Value, Version: reference.Snapshot.Version, Title: reference.Snapshot.Title,
		Actor: actor, CreatedLabel: reference.CreatedAt.In(location).Format("2006-01-02 15:04:05"),
		StatusLabel: contentStatusLabel(reference.Snapshot.Status), Current: reference.Current,
	}
}

func revisionCompareOptions(revisions []content.Revision, from, to revisionCompareReference, currentVersion int64) []adminRevisionOptionView {
	byID := make(map[int64]content.Revision, len(revisions)+2)
	for _, revision := range revisions {
		byID[revision.ID] = revision
	}
	for _, reference := range []revisionCompareReference{from, to} {
		if reference.Current {
			continue
		}
		id, err := strconv.ParseInt(reference.Value, 10, 64)
		if err == nil {
			byID[id] = content.Revision{ID: id, Version: reference.Snapshot.Version, Snapshot: reference.Snapshot, Actor: reference.Actor, CreatedAt: reference.CreatedAt}
		}
	}
	items := make([]content.Revision, 0, len(byID))
	for _, revision := range byID {
		items = append(items, revision)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Version == items[j].Version {
			return items[i].ID > items[j].ID
		}
		return items[i].Version > items[j].Version
	})
	options := make([]adminRevisionOptionView, 0, len(items)+1)
	options = append(options, adminRevisionOptionView{
		Value: "current", Label: "当前存储 · v" + strconv.FormatInt(currentVersion, 10),
		SelectedTo: to.Value == "current", Current: true,
	})
	for _, revision := range items {
		value := strconv.FormatInt(revision.ID, 10)
		label := "v" + strconv.FormatInt(revision.Version, 10) + " · " + revision.Snapshot.Title
		options = append(options, adminRevisionOptionView{
			Value: value, Label: label, SelectedFrom: from.Value == value, SelectedTo: to.Value == value,
		})
	}
	return options
}

func revisionFieldComparison(from, to content.Post, location *time.Location) ([]adminRevisionFieldView, int) {
	fromValues := revisionFieldValues(from, location)
	toValues := revisionFieldValues(to, location)
	labels := []string{"标题", "公开别名", "内容类型", "工作流状态", "摘要", "分类", "标签", "封面资源", "评论", "署名", "发布时间"}
	fields := make([]adminRevisionFieldView, 0, len(labels))
	changed := 0
	for _, label := range labels {
		field := adminRevisionFieldView{Label: label, From: fromValues[label], To: toValues[label]}
		field.Changed = field.From != field.To
		if field.Changed {
			changed++
		}
		fields = append(fields, field)
	}
	return fields, changed
}

func revisionFieldValues(post content.Post, location *time.Location) map[string]string {
	valueOrEmpty := func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "未设置"
		}
		return value
	}
	categoryNames := make([]string, 0, len(post.Categories))
	for _, term := range post.Categories {
		categoryNames = append(categoryNames, term.Name)
	}
	if len(categoryNames) == 0 && post.Category != "" {
		categoryNames = append(categoryNames, post.Category)
	}
	tagNames := make([]string, 0, len(post.Tags))
	for _, term := range post.Tags {
		tagNames = append(tagNames, "#"+term.Name)
	}
	published := "未设置"
	if !post.PublishedAt.IsZero() {
		published = post.PublishedAt.In(location).Format("2006-01-02 15:04:05")
	}
	commentLabel := "关闭"
	if post.CommentStatus == content.CommentOpen {
		commentLabel = "开启"
	}
	return map[string]string{
		"标题": post.Title, "公开别名": post.Slug, "内容类型": contentTypeLabel(post.Type),
		"工作流状态": contentStatusLabel(post.Status), "摘要": valueOrEmpty(post.Excerpt),
		"分类": valueOrEmpty(strings.Join(categoryNames, " / ")), "标签": valueOrEmpty(strings.Join(tagNames, " ")),
		"封面资源": valueOrEmpty(post.CoverURL), "评论": commentLabel, "署名": valueOrEmpty(post.AuthorName), "发布时间": published,
	}
}

func buildRevisionBodyDiff(from, to string) ([]adminRevisionDiffRowView, int, int, int, bool) {
	fromUnits, fromTruncated := revisionDiffUnits(from)
	toUnits, toTruncated := revisionDiffUnits(to)
	operations := revisionDiffOperations(fromUnits, toUnits)
	rows, added, removed, changed := pairRevisionDiffOperations(operations)
	return collapseRevisionEqualRows(rows), added, removed, changed, fromTruncated || toTruncated
}

func revisionDiffUnits(source string) ([]string, bool) {
	truncated := len(source) > revisionDiffMaxSourceBytes
	if len(source) > revisionDiffMaxSourceBytes {
		limit := revisionDiffMaxSourceBytes
		for limit > 0 && limit < len(source) && source[limit]&0xc0 == 0x80 {
			limit--
		}
		source = source[:limit]
	}
	source = strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
	units := make([]string, 0, min(revisionDiffMaxUnits, 64))
	buffer := make([]rune, 0, revisionDiffMaxUnitRunes)
	flush := func() bool {
		value := strings.TrimSpace(string(buffer))
		buffer = buffer[:0]
		if value == "" {
			return true
		}
		if len(units) >= revisionDiffMaxUnits {
			truncated = true
			return false
		}
		units = append(units, value)
		return true
	}
	for _, character := range source {
		if character == '\n' {
			if !flush() {
				break
			}
			continue
		}
		buffer = append(buffer, character)
		if len(buffer) >= revisionDiffMaxUnitRunes && !flush() {
			break
		}
	}
	if len(buffer) > 0 {
		_ = flush()
	}
	return units, truncated
}

func revisionDiffOperations(from, to []string) []revisionDiffOperation {
	width := len(to) + 1
	matrix := make([]uint16, (len(from)+1)*width)
	for i := len(from) - 1; i >= 0; i-- {
		for j := len(to) - 1; j >= 0; j-- {
			index := i*width + j
			if from[i] == to[j] {
				matrix[index] = matrix[(i+1)*width+j+1] + 1
			} else if matrix[(i+1)*width+j] >= matrix[i*width+j+1] {
				matrix[index] = matrix[(i+1)*width+j]
			} else {
				matrix[index] = matrix[i*width+j+1]
			}
		}
	}
	operations := make([]revisionDiffOperation, 0, len(from)+len(to))
	for i, j := 0, 0; i < len(from) || j < len(to); {
		switch {
		case i < len(from) && j < len(to) && from[i] == to[j]:
			operations = append(operations, revisionDiffOperation{Kind: "equal", Text: from[i]})
			i++
			j++
		case j < len(to) && (i == len(from) || matrix[i*width+j+1] > matrix[(i+1)*width+j]):
			operations = append(operations, revisionDiffOperation{Kind: "insert", Text: to[j]})
			j++
		default:
			operations = append(operations, revisionDiffOperation{Kind: "delete", Text: from[i]})
			i++
		}
	}
	return operations
}

func pairRevisionDiffOperations(operations []revisionDiffOperation) ([]adminRevisionDiffRowView, int, int, int) {
	rows := make([]adminRevisionDiffRowView, 0, len(operations))
	fromLine, toLine := 1, 1
	added, removed, changed := 0, 0, 0
	for index := 0; index < len(operations); {
		if operations[index].Kind == "equal" {
			rows = append(rows, adminRevisionDiffRowView{
				Kind: "equal", FromLine: fromLine, ToLine: toLine,
				FromText: operations[index].Text, ToText: operations[index].Text,
			})
			fromLine++
			toLine++
			index++
			continue
		}
		deletes := make([]string, 0, 2)
		inserts := make([]string, 0, 2)
		for index < len(operations) && operations[index].Kind != "equal" {
			if operations[index].Kind == "delete" {
				deletes = append(deletes, operations[index].Text)
			} else {
				inserts = append(inserts, operations[index].Text)
			}
			index++
		}
		for pair := 0; pair < max(len(deletes), len(inserts)); pair++ {
			row := adminRevisionDiffRowView{}
			if pair < len(deletes) {
				row.FromLine = fromLine
				row.FromText = deletes[pair]
				fromLine++
			}
			if pair < len(inserts) {
				row.ToLine = toLine
				row.ToText = inserts[pair]
				toLine++
			}
			switch {
			case row.FromText != "" && row.ToText != "":
				row.Kind = "changed"
				changed++
			case row.FromText != "":
				row.Kind = "removed"
				removed++
			default:
				row.Kind = "added"
				added++
			}
			rows = append(rows, row)
		}
	}
	return rows, added, removed, changed
}

func collapseRevisionEqualRows(rows []adminRevisionDiffRowView) []adminRevisionDiffRowView {
	result := make([]adminRevisionDiffRowView, 0, len(rows))
	for start := 0; start < len(rows); {
		if rows[start].Kind != "equal" {
			result = append(result, rows[start])
			start++
			continue
		}
		end := start
		for end < len(rows) && rows[end].Kind == "equal" {
			end++
		}
		run := rows[start:end]
		if len(run) <= revisionDiffContextUnits*2+1 {
			result = append(result, run...)
		} else {
			result = append(result, run[:revisionDiffContextUnits]...)
			result = append(result, adminRevisionDiffRowView{Kind: "collapsed", Collapsed: len(run) - revisionDiffContextUnits*2})
			result = append(result, run[len(run)-revisionDiffContextUnits:]...)
		}
		start = end
	}
	return result
}
