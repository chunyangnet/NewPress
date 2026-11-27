package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/comment"
)

type CommentRepository struct {
	pool *pgxpool.Pool
}

func NewCommentRepository(pool *pgxpool.Pool) *CommentRepository {
	return &CommentRepository{pool: pool}
}

func (repository *CommentRepository) Ready(ctx context.Context) error {
	if repository == nil || repository.pool == nil {
		return errors.New("comment repository is nil")
	}
	return repository.pool.Ping(ctx)
}

func (repository *CommentRepository) ListApproved(ctx context.Context, postID int64, limit, offset int) ([]comment.Comment, int, error) {
	if repository == nil || repository.pool == nil || postID <= 0 || offset < 0 {
		return nil, 0, comment.ErrInvalidInput
	}
	if limit <= 0 || limit > comment.MaxListLimit {
		limit = 50
	}
	var total int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM np_comments WHERE post_id=$1 AND status='approved'`, postID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count approved comments: %w", err)
	}
	rows, err := repository.pool.Query(ctx, `SELECT `+commentColumns+`
		FROM np_comments WHERE post_id=$1 AND status='approved'
		ORDER BY created_at,id LIMIT $2 OFFSET $3`, postID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list approved comments: %w", err)
	}
	defer rows.Close()
	items := make([]comment.Comment, 0, min(limit, total))
	for rows.Next() {
		item, err := scanComment(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan approved comment: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate approved comments: %w", err)
	}
	return items, total, nil
}

func (repository *CommentRepository) ListAdmin(ctx context.Context, query comment.Query) (comment.List, error) {
	if repository == nil || repository.pool == nil {
		return comment.List{}, errors.New("comment repository is nil")
	}
	normalized, err := comment.NormalizeQuery(query)
	if err != nil {
		return comment.List{}, err
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 5)
	add := func(fragment string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(fragment, len(args)))
	}
	if normalized.PostID > 0 {
		add("post_id=$%d", normalized.PostID)
	}
	if normalized.UserID > 0 {
		add("user_id=$%d", normalized.UserID)
	}
	if normalized.OwnOnly {
		conditions = append(conditions, "status IN ('approved','pending')")
	}
	if normalized.Status != "" {
		add("status=$%d", normalized.Status)
	}
	if normalized.Search != "" {
		args = append(args, normalized.Search)
		placeholder := len(args)
		if normalized.OwnOnly {
			conditions = append(conditions, fmt.Sprintf("strpos(lower(content_source),lower($%d))>0", placeholder))
		} else {
			conditions = append(conditions, fmt.Sprintf("(author_name ILIKE '%%' || $%[1]d || '%%' OR author_email ILIKE '%%' || $%[1]d || '%%' OR content_source ILIKE '%%' || $%[1]d || '%%')", placeholder))
		}
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := repository.pool.QueryRow(ctx, "SELECT count(*) FROM np_comments"+where, args...).Scan(&total); err != nil {
		return comment.List{}, fmt.Errorf("count admin comments: %w", err)
	}
	itemArgs := append([]any(nil), args...)
	limitPlaceholder := len(itemArgs) + 1
	offsetPlaceholder := len(itemArgs) + 2
	itemArgs = append(itemArgs, normalized.Limit, normalized.Offset)
	rows, err := repository.pool.Query(ctx, `SELECT `+commentColumns+` FROM np_comments`+where+
		fmt.Sprintf(" ORDER BY updated_at DESC,id DESC LIMIT $%d OFFSET $%d", limitPlaceholder, offsetPlaceholder), itemArgs...)
	if err != nil {
		return comment.List{}, fmt.Errorf("list admin comments: %w", err)
	}
	items := make([]comment.Comment, 0, min(normalized.Limit, total))
	for rows.Next() {
		item, scanErr := scanComment(rows)
		if scanErr != nil {
			rows.Close()
			return comment.List{}, fmt.Errorf("scan admin comment: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return comment.List{}, fmt.Errorf("iterate admin comments: %w", err)
	}
	rows.Close()
	counts := map[string]int{comment.StatusPending: 0, comment.StatusApproved: 0, comment.StatusSpam: 0, comment.StatusTrash: 0}
	countSQL, countArgs := `SELECT status,count(*) FROM np_comments`, []any{}
	if normalized.UserID > 0 {
		countSQL += " WHERE user_id=$1"
		countArgs = append(countArgs, normalized.UserID)
	}
	if normalized.OwnOnly {
		countSQL += " AND status IN ('approved','pending')"
	}
	countRows, err := repository.pool.Query(ctx, countSQL+` GROUP BY status`, countArgs...)
	if err != nil {
		return comment.List{}, fmt.Errorf("count comment states: %w", err)
	}
	for countRows.Next() {
		var status string
		var count int
		if err := countRows.Scan(&status, &count); err != nil {
			countRows.Close()
			return comment.List{}, fmt.Errorf("scan comment state count: %w", err)
		}
		if comment.ValidStatus(status) {
			counts[status] = count
		}
	}
	if err := countRows.Err(); err != nil {
		countRows.Close()
		return comment.List{}, fmt.Errorf("iterate comment state counts: %w", err)
	}
	countRows.Close()
	return comment.List{Items: items, Total: total, StatusCounts: counts}, nil
}

func (repository *CommentRepository) Get(ctx context.Context, id int64) (comment.Comment, error) {
	if repository == nil || repository.pool == nil || id <= 0 {
		return comment.Comment{}, comment.ErrNotFound
	}
	item, err := scanComment(repository.pool.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("get comment: %w", err)
	}
	return item, nil
}

func (repository *CommentRepository) Create(ctx context.Context, input comment.CreateInput) (comment.Comment, error) {
	if repository == nil || repository.pool == nil {
		return comment.Comment{}, errors.New("comment repository is nil")
	}
	normalized, fields := comment.NormalizeCreateInput(input)
	if len(fields) > 0 {
		return comment.Comment{}, &comment.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.Comment{}, fmt.Errorf("begin create comment: %w", err)
	}
	defer tx.Rollback(ctx)
	item, err := createCommentInTransaction(ctx, tx, normalized)
	if err != nil {
		return comment.Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.Comment{}, fmt.Errorf("commit comment: %w", err)
	}
	return item, nil
}

func createCommentInTransaction(ctx context.Context, tx pgx.Tx, normalized comment.CreateInput) (comment.Comment, error) {
	if err := validateCommentParent(ctx, tx, normalized.PostID, normalized.ParentID); err != nil {
		return comment.Comment{}, err
	}
	now := time.Now().UTC()
	item, err := scanComment(tx.QueryRow(ctx, `INSERT INTO np_comments(
		post_id,parent_id,user_id,author_name,author_email,status,content_source,content_html,
		author_ip_hash,user_agent,created_at,updated_at,version
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,1)
	RETURNING `+commentColumns,
		normalized.PostID, nullablePositiveID(normalized.ParentID), nullablePositiveID(normalized.UserID),
		normalized.AuthorName, normalized.AuthorEmail, normalized.Status, normalized.ContentSource,
		comment.RenderPlainText(normalized.ContentSource), normalized.AuthorIPHash, normalized.UserAgent, now))
	if err != nil {
		return comment.Comment{}, fmt.Errorf("insert comment: %w", err)
	}
	if err := insertActivity(ctx, tx, "notification.comment", comment.ActivityEvents(comment.Comment{}, item)); err != nil {
		return comment.Comment{}, err
	}
	return item, nil
}

func (repository *CommentRepository) Edit(ctx context.Context, id int64, mutation comment.Mutation, input comment.EditInput) (comment.Comment, error) {
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return comment.Comment{}, comment.ErrInvalidInput
	}
	actor, actorOK := comment.NormalizeActor(mutation.Actor)
	if !actorOK {
		return comment.Comment{}, &comment.ValidationError{Fields: map[string]string{"actor": "编辑者名称需为 1–160 个字符且不能包含控制字符"}}
	}
	normalized, fields := comment.NormalizeEditInput(input)
	if len(fields) > 0 {
		return comment.Comment{}, &comment.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.Comment{}, fmt.Errorf("begin edit comment: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanComment(tx.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("lock comment for edit: %w", err)
	}
	if current.Version != mutation.ExpectedVersion {
		return comment.Comment{}, comment.ErrVersionConflict
	}
	if current.AuthorName == normalized.AuthorName && current.AuthorEmail == normalized.AuthorEmail && current.ContentSource == normalized.ContentSource {
		return current, nil
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO np_comment_revisions(
		comment_id,version,author_name,author_email,status,content_source,content_html,actor_label,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, current.ID, current.Version, current.AuthorName,
		current.AuthorEmail, current.Status, current.ContentSource, current.ContentHTML, actor, now); err != nil {
		return comment.Comment{}, fmt.Errorf("record comment revision: %w", err)
	}
	updated, err := scanComment(tx.QueryRow(ctx, `UPDATE np_comments SET
		author_name=$2,author_email=$3,content_source=$4,content_html=$5,
		version=version+1,updated_at=$6,edited_by=$7,edited_at=$6
		WHERE id=$1 AND version=$8 RETURNING `+commentColumns,
		id, normalized.AuthorName, normalized.AuthorEmail, normalized.ContentSource,
		comment.RenderPlainText(normalized.ContentSource), now, actor, mutation.ExpectedVersion))
	if err != nil {
		return comment.Comment{}, fmt.Errorf("edit comment: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"post_id": current.PostID, "from_version": current.Version, "to_version": updated.Version,
		"changed_fields": commentEditChangedFields(current, normalized),
	})
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,'comment.edit','comment',$2,$3)`, actor, fmt.Sprint(id), metadata); err != nil {
		return comment.Comment{}, fmt.Errorf("record comment edit audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.Comment{}, fmt.Errorf("commit comment edit: %w", err)
	}
	return updated, nil
}

func (repository *CommentRepository) RestoreRevision(ctx context.Context, id, sourceVersion int64, mutation comment.Mutation) (comment.Comment, error) {
	if repository == nil || repository.pool == nil || id <= 0 || sourceVersion <= 0 || mutation.ExpectedVersion <= 0 {
		return comment.Comment{}, comment.ErrInvalidInput
	}
	actor, actorOK := comment.NormalizeActor(mutation.Actor)
	if !actorOK {
		return comment.Comment{}, &comment.ValidationError{Fields: map[string]string{"actor": "恢复者名称需为 1–160 个字符且不能包含控制字符"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.Comment{}, fmt.Errorf("begin restore comment revision: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanComment(tx.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("lock comment for revision restore: %w", err)
	}
	if current.Version != mutation.ExpectedVersion {
		return comment.Comment{}, comment.ErrVersionConflict
	}
	var source comment.Revision
	err = tx.QueryRow(ctx, `SELECT comment_id,version,author_name,author_email,status,
		content_source,content_html,actor_label,created_at
		FROM np_comment_revisions WHERE comment_id=$1 AND version=$2`, id, sourceVersion).Scan(
		&source.CommentID, &source.Version, &source.AuthorName, &source.AuthorEmail, &source.Status,
		&source.ContentSource, &source.ContentHTML, &source.Actor, &source.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrRevisionNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("load comment revision for restore: %w", err)
	}
	normalized, fields := comment.NormalizeEditInput(comment.EditInput{
		AuthorName: source.AuthorName, AuthorEmail: source.AuthorEmail, ContentSource: source.ContentSource,
	})
	if len(fields) > 0 {
		return comment.Comment{}, &comment.ValidationError{Fields: fields}
	}
	if current.AuthorName == normalized.AuthorName && current.AuthorEmail == normalized.AuthorEmail && current.ContentSource == normalized.ContentSource {
		return current, nil
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO np_comment_revisions(
		comment_id,version,author_name,author_email,status,content_source,content_html,actor_label,created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, current.ID, current.Version, current.AuthorName,
		current.AuthorEmail, current.Status, current.ContentSource, current.ContentHTML, actor, now); err != nil {
		return comment.Comment{}, fmt.Errorf("record pre-restore comment revision: %w", err)
	}
	updated, err := scanComment(tx.QueryRow(ctx, `UPDATE np_comments SET
		author_name=$2,author_email=$3,content_source=$4,content_html=$5,
		version=version+1,updated_at=$6,edited_by=$7,edited_at=$6
		WHERE id=$1 AND version=$8 RETURNING `+commentColumns,
		id, normalized.AuthorName, normalized.AuthorEmail, normalized.ContentSource,
		comment.RenderPlainText(normalized.ContentSource), now, actor, mutation.ExpectedVersion))
	if err != nil {
		return comment.Comment{}, fmt.Errorf("restore comment revision: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"post_id": current.PostID, "source_version": sourceVersion, "from_version": current.Version,
		"to_version": updated.Version, "preserved_status": current.Status,
		"changed_fields": commentEditChangedFields(current, normalized),
	})
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,'comment.revision.restore','comment',$2,$3)`, actor, fmt.Sprint(id), metadata); err != nil {
		return comment.Comment{}, fmt.Errorf("record comment revision restore audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.Comment{}, fmt.Errorf("commit comment revision restore: %w", err)
	}
	return updated, nil
}

func (repository *CommentRepository) Reply(ctx context.Context, input comment.ReplyInput) (comment.Comment, error) {
	if repository == nil || repository.pool == nil {
		return comment.Comment{}, errors.New("comment repository is nil")
	}
	normalized, fields := comment.NormalizeReplyInput(input)
	if len(fields) > 0 {
		return comment.Comment{}, &comment.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.Comment{}, fmt.Errorf("begin staff comment reply: %w", err)
	}
	defer tx.Rollback(ctx)
	parent, err := scanComment(tx.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1 FOR UPDATE`, normalized.ParentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("lock staff reply parent: %w", err)
	}
	if parent.Version != normalized.ExpectedParentVersion {
		return comment.Comment{}, comment.ErrVersionConflict
	}
	if err := validateStaffCommentReplyParent(ctx, tx, parent); err != nil {
		return comment.Comment{}, err
	}
	now := time.Now().UTC()
	item, err := scanComment(tx.QueryRow(ctx, `INSERT INTO np_comments(
		post_id,parent_id,user_id,author_name,author_email,status,content_source,content_html,
		author_ip_hash,user_agent,staff_reply,created_by,moderated_by,moderated_at,created_at,updated_at,version
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'','',true,$9,$9,$10,$10,$10,1)
	RETURNING `+commentColumns,
		parent.PostID, parent.ID, nullablePositiveID(normalized.UserID), normalized.AuthorName, normalized.AuthorEmail,
		parent.Status, normalized.ContentSource, comment.RenderPlainText(normalized.ContentSource), normalized.Actor, now))
	if err != nil {
		return comment.Comment{}, fmt.Errorf("insert staff comment reply: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"post_id": item.PostID, "parent_id": parent.ID, "status": item.Status,
		"parent_version": parent.Version, "version": item.Version,
	})
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,'comment.reply','comment',$2,$3)`, normalized.Actor, fmt.Sprint(item.ID), metadata); err != nil {
		return comment.Comment{}, fmt.Errorf("record staff comment reply audit: %w", err)
	}
	if err := insertActivity(ctx, tx, "notification.comment", comment.ActivityEvents(comment.Comment{}, item)); err != nil {
		return comment.Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.Comment{}, fmt.Errorf("commit staff comment reply: %w", err)
	}
	return item, nil
}

func (repository *CommentRepository) ListRevisions(ctx context.Context, id int64, limit int) ([]comment.Revision, error) {
	if repository == nil || repository.pool == nil || id <= 0 {
		return nil, comment.ErrNotFound
	}
	limit = comment.NormalizeRevisionLimit(limit)
	var exists bool
	if err := repository.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_comments WHERE id=$1)`, id).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check comment revisions owner: %w", err)
	}
	if !exists {
		return nil, comment.ErrNotFound
	}
	rows, err := repository.pool.Query(ctx, `SELECT comment_id,version,author_name,author_email,status,
		content_source,content_html,actor_label,created_at
		FROM np_comment_revisions WHERE comment_id=$1 ORDER BY version DESC LIMIT $2`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("list comment revisions: %w", err)
	}
	defer rows.Close()
	items := make([]comment.Revision, 0, limit)
	for rows.Next() {
		var revision comment.Revision
		if err := rows.Scan(&revision.CommentID, &revision.Version, &revision.AuthorName, &revision.AuthorEmail,
			&revision.Status, &revision.ContentSource, &revision.ContentHTML, &revision.Actor, &revision.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment revision: %w", err)
		}
		revision.CreatedAt = revision.CreatedAt.UTC()
		items = append(items, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comment revisions: %w", err)
	}
	return items, nil
}

func (repository *CommentRepository) ChangeStatus(ctx context.Context, id int64, mutation comment.Mutation, status string) (comment.Comment, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 || !comment.ValidStatus(status) {
		return comment.Comment{}, comment.ErrInvalidInput
	}
	actor, actorOK := comment.NormalizeActor(mutation.Actor)
	if actor != "" && !actorOK {
		return comment.Comment{}, &comment.ValidationError{Fields: map[string]string{"actor": "审核者名称不能超过 160 个字符，且必须是无控制字符的有效 UTF-8 文本"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return comment.Comment{}, fmt.Errorf("begin moderate comment: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanComment(tx.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.Comment{}, comment.ErrNotFound
	}
	if err != nil {
		return comment.Comment{}, fmt.Errorf("lock comment: %w", err)
	}
	if current.Version != mutation.ExpectedVersion {
		return comment.Comment{}, comment.ErrVersionConflict
	}
	now := time.Now().UTC()
	item, err := scanComment(tx.QueryRow(ctx, `UPDATE np_comments SET
		status=$2,version=version+1,updated_at=$3,moderated_by=$4,moderated_at=$3
		WHERE id=$1 AND version=$5 RETURNING `+commentColumns,
		id, status, now, actor, mutation.ExpectedVersion))
	if err != nil {
		return comment.Comment{}, fmt.Errorf("update comment status: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"post_id": current.PostID, "from": current.Status, "to": status, "version": item.Version,
	})
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,$2,'comment',$3,$4)`, actor, "comment.status."+status, fmt.Sprint(id), metadata); err != nil {
		return comment.Comment{}, fmt.Errorf("record comment audit: %w", err)
	}
	if err := insertActivity(ctx, tx, "notification.comment", comment.ActivityEvents(current, item)); err != nil {
		return comment.Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return comment.Comment{}, fmt.Errorf("commit comment moderation: %w", err)
	}
	return item, nil
}

func (repository *CommentRepository) BulkChangeStatus(ctx context.Context, change comment.BulkStatusChange) ([]comment.Comment, error) {
	if repository == nil || repository.pool == nil {
		return nil, errors.New("comment repository is nil")
	}
	normalized, fields := comment.NormalizeBulkStatusChange(change)
	if len(fields) > 0 {
		return nil, &comment.ValidationError{Fields: fields}
	}
	ids := make([]int64, len(normalized.Items))
	expectedVersions := make(map[int64]int64, len(normalized.Items))
	for index, item := range normalized.Items {
		ids[index] = item.ID
		expectedVersions[item.ID] = item.ExpectedVersion
	}

	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin bulk comment moderation: %w", err)
	}
	defer tx.Rollback(ctx)

	// NormalizeBulkStatusChange sorts IDs, so all concurrent batch moderators
	// acquire row locks in the same order. No write is attempted until the
	// complete set and every optimistic version have been verified.
	rows, err := tx.Query(ctx, `SELECT `+commentColumns+`
		FROM np_comments
		WHERE id=ANY($1::bigint[])
		ORDER BY id
		FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock bulk comments: %w", err)
	}
	locked := make([]comment.Comment, 0, len(ids))
	for rows.Next() {
		item, scanErr := scanComment(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan locked bulk comment: %w", scanErr)
		}
		locked = append(locked, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate locked bulk comments: %w", err)
	}
	rows.Close()
	if len(locked) != len(ids) {
		return nil, comment.ErrNotFound
	}
	for _, item := range locked {
		if item.Version != expectedVersions[item.ID] {
			return nil, comment.ErrVersionConflict
		}
	}

	now := time.Now().UTC()
	actor := normalized.Actor
	command, err := tx.Exec(ctx, `UPDATE np_comments SET
		status=$2,version=version+1,updated_at=$3,moderated_by=$4,moderated_at=$3
		WHERE id=ANY($1::bigint[])`, ids, normalized.Status, now, actor)
	if err != nil {
		return nil, fmt.Errorf("bulk update comment status: %w", err)
	}
	if command.RowsAffected() != int64(len(ids)) {
		return nil, comment.ErrVersionConflict
	}

	rows, err = tx.Query(ctx, `SELECT `+commentColumns+`
		FROM np_comments
		WHERE id=ANY($1::bigint[])
		ORDER BY id`, ids)
	if err != nil {
		return nil, fmt.Errorf("load bulk moderated comments: %w", err)
	}
	updated := make([]comment.Comment, 0, len(ids))
	for rows.Next() {
		item, scanErr := scanComment(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan bulk moderated comment: %w", scanErr)
		}
		updated = append(updated, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate bulk moderated comments: %w", err)
	}
	rows.Close()
	if len(updated) != len(ids) {
		return nil, fmt.Errorf("load bulk moderated comments: expected %d rows, got %d", len(ids), len(updated))
	}
	for index, item := range updated {
		if err := insertActivity(ctx, tx, "notification.comment", comment.ActivityEvents(locked[index], item)); err != nil {
			return nil, err
		}
		metadata, _ := json.Marshal(map[string]any{
			"post_id": locked[index].PostID, "from": locked[index].Status, "to": normalized.Status, "version": item.Version,
			"bulk_size": len(updated),
		})
		if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
			VALUES ($1,$2,'comment',$3,$4)`, actor, "comment.status."+normalized.Status, fmt.Sprint(item.ID), metadata); err != nil {
			return nil, fmt.Errorf("record bulk comment audit: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bulk comment moderation: %w", err)
	}
	return updated, nil
}

func (repository *CommentRepository) Delete(ctx context.Context, id int64, mutation comment.Mutation) error {
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return comment.ErrInvalidInput
	}
	actor, actorOK := comment.NormalizeActor(mutation.Actor)
	if actor != "" && !actorOK {
		return &comment.ValidationError{Fields: map[string]string{"actor": "操作者名称不能超过 160 个字符，且必须是无控制字符的有效 UTF-8 文本"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete comment: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := scanComment(tx.QueryRow(ctx, `SELECT `+commentColumns+` FROM np_comments WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return comment.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock comment for deletion: %w", err)
	}
	if current.Version != mutation.ExpectedVersion {
		return comment.ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_comments WHERE id=$1 AND version=$2`, id, mutation.ExpectedVersion); err != nil {
		return fmt.Errorf("delete comment: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{"post_id": current.PostID, "status": current.Status, "version": current.Version})
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,'comment.delete','comment',$2,$3)`, actor, fmt.Sprint(id), metadata); err != nil {
		return fmt.Errorf("record comment deletion audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit comment deletion: %w", err)
	}
	return nil
}

func (repository *CommentRepository) Snapshot(ctx context.Context) (comment.Snapshot, error) {
	if repository == nil || repository.pool == nil {
		return comment.Snapshot{}, errors.New("comment repository is nil")
	}
	counts := map[string]int{comment.StatusPending: 0, comment.StatusApproved: 0, comment.StatusSpam: 0, comment.StatusTrash: 0}
	rows, err := repository.pool.Query(ctx, `SELECT status,count(*) FROM np_comments GROUP BY status`)
	if err != nil {
		return comment.Snapshot{}, fmt.Errorf("snapshot comment states: %w", err)
	}
	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return comment.Snapshot{}, fmt.Errorf("scan comment snapshot: %w", err)
		}
		if comment.ValidStatus(status) {
			counts[status] = count
			total += count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return comment.Snapshot{}, fmt.Errorf("iterate comment snapshot: %w", err)
	}
	rows.Close()
	var updatedAt time.Time
	if err := repository.pool.QueryRow(ctx, `SELECT COALESCE(max(updated_at),to_timestamp(0)) FROM np_comments`).Scan(&updatedAt); err != nil {
		return comment.Snapshot{}, fmt.Errorf("load comment snapshot time: %w", err)
	}
	return comment.Snapshot{Total: total, StatusCounts: counts, UpdatedAt: updatedAt.UTC()}, nil
}

func validateCommentParent(ctx context.Context, tx pgx.Tx, postID, parentID int64) error {
	if parentID == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, comment.MaxThreadDepth)
	currentID := parentID
	for depth := 1; currentID > 0; depth++ {
		if depth >= comment.MaxThreadDepth {
			return comment.ErrInvalidParent
		}
		if _, exists := seen[currentID]; exists {
			return comment.ErrInvalidParent
		}
		seen[currentID] = struct{}{}
		var nextID, parentPostID int64
		var status string
		err := tx.QueryRow(ctx, `SELECT COALESCE(parent_id,0),post_id,status FROM np_comments WHERE id=$1 FOR SHARE`, currentID).Scan(&nextID, &parentPostID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return comment.ErrInvalidParent
		}
		if err != nil {
			return fmt.Errorf("validate comment parent: %w", err)
		}
		if parentPostID != postID || status != comment.StatusApproved {
			return comment.ErrInvalidParent
		}
		currentID = nextID
	}
	return nil
}

func validateStaffCommentReplyParent(ctx context.Context, tx pgx.Tx, parent comment.Comment) error {
	if parent.Status != comment.StatusApproved && parent.Status != comment.StatusPending {
		return comment.ErrInvalidParent
	}
	seen := make(map[int64]struct{}, comment.MaxThreadDepth)
	currentID := parent.ID
	for depth := 1; currentID > 0; depth++ {
		if depth >= comment.MaxThreadDepth {
			return comment.ErrInvalidParent
		}
		if _, exists := seen[currentID]; exists {
			return comment.ErrInvalidParent
		}
		seen[currentID] = struct{}{}
		if currentID == parent.ID {
			currentID = parent.ParentID
			continue
		}
		var nextID, postID int64
		var status string
		err := tx.QueryRow(ctx, `SELECT COALESCE(parent_id,0),post_id,status FROM np_comments WHERE id=$1 FOR SHARE`, currentID).Scan(&nextID, &postID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return comment.ErrInvalidParent
		}
		if err != nil {
			return fmt.Errorf("validate staff reply ancestry: %w", err)
		}
		if postID != parent.PostID || (status != comment.StatusApproved && status != comment.StatusPending) {
			return comment.ErrInvalidParent
		}
		currentID = nextID
	}
	return nil
}

func commentEditChangedFields(current comment.Comment, input comment.EditInput) []string {
	fields := make([]string, 0, 3)
	if current.AuthorName != input.AuthorName {
		fields = append(fields, "author_name")
	}
	if current.AuthorEmail != input.AuthorEmail {
		fields = append(fields, "author_email")
	}
	if current.ContentSource != input.ContentSource {
		fields = append(fields, "content_source")
	}
	return fields
}

const commentColumns = `
	id,post_id,COALESCE(parent_id,0),COALESCE(user_id,0),version,
	author_name,author_email,status,content_source,content_html,
	author_ip_hash,user_agent,staff_reply,created_by,edited_by,edited_at,
	moderated_by,moderated_at,created_at,updated_at`

func scanComment(row scanner) (comment.Comment, error) {
	var item comment.Comment
	var editedAt, moderatedAt *time.Time
	if err := row.Scan(
		&item.ID, &item.PostID, &item.ParentID, &item.UserID, &item.Version,
		&item.AuthorName, &item.AuthorEmail, &item.Status, &item.ContentSource, &item.ContentHTML,
		&item.AuthorIPHash, &item.UserAgent, &item.StaffReply, &item.CreatedBy, &item.EditedBy, &editedAt,
		&item.ModeratedBy, &moderatedAt, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return comment.Comment{}, err
	}
	if editedAt != nil {
		item.EditedAt = editedAt.UTC()
	}
	if moderatedAt != nil {
		item.ModeratedAt = moderatedAt.UTC()
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

func nullablePositiveID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

var _ comment.Store = (*CommentRepository)(nil)
