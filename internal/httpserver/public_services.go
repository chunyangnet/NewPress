package httpserver

import "newpress.local/newpress/internal/presentation"

func publicAnnouncements(v announcementPublicView) presentation.AnnouncementPage {
	p := presentation.AnnouncementPage{Current: presentation.AnnouncementItem(v.Current), RelatedURL: v.RelatedURL, Query: v.Query, Scope: v.Scope, Total: v.Total, PinnedTotal: v.PinnedTotal, Results: v.Results, Page: v.Page, Pages: v.Pages, Previous: v.Previous, Next: v.Next}
	for _, item := range v.Items {
		p.Items = append(p.Items, presentation.AnnouncementItem(item))
	}
	for _, paragraph := range v.Paragraphs {
		p.Paragraphs = append(p.Paragraphs, presentation.AnnouncementParagraph(paragraph))
	}
	for _, link := range v.PageLinks {
		p.PageLinks = append(p.PageLinks, presentation.AnnouncementPageLink(link))
	}
	if v.PreviousItem != nil {
		item := presentation.AnnouncementItem(*v.PreviousItem)
		p.PreviousItem = &item
	}
	if v.NextItem != nil {
		item := presentation.AnnouncementItem(*v.NextItem)
		p.NextItem = &item
	}
	return p
}

func publicQuestionNotes(items []questionNoteView) []presentation.QuestionNote {
	result := make([]presentation.QuestionNote, 0, len(items))
	for _, n := range items {
		result = append(result, presentation.QuestionNote{ID: n.ID, AuthorID: n.AuthorID, Author: n.Author, Body: n.Body, At: n.At})
	}
	return result
}

func publicQuestion(q questionView) presentation.Question {
	p := presentation.Question{ID: q.ID, Version: q.Version, AuthorID: q.AuthorID, AcceptedID: q.AcceptedID,
		Title: q.Title, HTML: string(q.HTML), URL: q.URL, Author: q.Author, AcceptedURL: q.AcceptedURL,
		AnswerPreview: q.AnswerPreview, Tags: append([]string(nil), q.Tags...), CreatedAt: q.CreatedAt,
		UpdatedAt: q.UpdatedAt, Hidden: q.Hidden, Locked: q.Locked, CanEdit: q.CanEdit, CanAccept: q.CanAccept,
		Count: q.Count, NotesView: publicQuestionNotes(q.NotesView)}
	if q.CanEdit {
		p.Body = q.Body
	}
	for _, a := range q.AnswersView {
		if a.Hidden {
			continue
		}
		answer := presentation.QuestionAnswer{ID: a.ID, AuthorID: a.AuthorID, HTML: string(a.HTML), Author: a.Author,
			CreatedAt: a.CreatedAt, EditedAt: a.EditedAt, CanEdit: a.CanEdit, Accepted: a.Accepted, NotesView: publicQuestionNotes(a.NotesView)}
		if a.CanEdit {
			answer.Body = a.Body
		}
		p.AnswersView = append(p.AnswersView, answer)
	}
	return p
}

func publicQuestions(v questionsView) presentation.Questions {
	p := presentation.Questions{Question: publicQuestion(v.Question), SignedIn: v.SignedIn, Manage: v.Manage, New: v.New,
		UserID: v.UserID, CSRF: v.CSRF, Query: v.Query, Status: v.Status, Tag: v.Tag, Scope: v.Scope, Sort: v.Sort,
		Tags: append([]string(nil), v.Tags...), Total: v.Total, Page: v.Page, Pages: v.Pages, Previous: v.Previous, Next: v.Next,
		PublicTotal: v.PublicTotal, OpenTotal: v.OpenTotal, SolvedTotal: v.SolvedTotal, AllURL: v.AllURL, OpenURL: v.OpenURL, SolvedURL: v.SolvedURL}
	for _, q := range v.Items {
		p.Items = append(p.Items, publicQuestion(q))
	}
	for _, q := range v.Featured {
		p.Featured = append(p.Featured, publicQuestion(q))
	}
	for _, topic := range v.Topics {
		p.Topics = append(p.Topics, presentation.QuestionTopic(topic))
	}
	return p
}

func publicFlash(v flashPageView) presentation.Flash {
	p := presentation.Flash{UserID: v.UserID, Current: presentation.FlashItem(v.Current), Total: v.Total, Query: v.Query,
		Filter: v.Filter, PreviousURL: v.PreviousURL, NextURL: v.NextURL, CSRF: v.CSRF, Endpoint: v.Endpoint,
		Authenticated: v.Authenticated, Admin: v.Admin, Editing: v.Editing}
	for _, item := range v.Items {
		p.Items = append(p.Items, presentation.FlashItem(item))
	}
	return p
}
