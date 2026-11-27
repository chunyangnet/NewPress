package plugin

import (
	"context"
	"encoding/json"

	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

type taskEffectInvocation struct {
	claim    pluginjobs.Job
	store    plugineffects.Store
	webhooks plugindispatch.Store
}

func (invocation *taskEffectInvocation) enqueueWebhook(ctx context.Context, control TaskControl, slot, url string, body json.RawMessage) (plugindispatch.Accepted, error) {
	manifest, err := json.Marshal(control.Manifest)
	if err != nil {
		return plugindispatch.Accepted{}, plugindispatch.ErrInvalid
	}
	request := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: invocation.claim, ManifestDigest: plugineffects.Digest(manifest), ModuleDigest: control.ModuleDigest}, Slot: slot, URL: url, Body: body}
	return invocation.webhooks.Enqueue(ctx, request)
}

func (invocation *taskEffectInvocation) writeContent(ctx context.Context, control TaskControl, slot string, postID int64, raw json.RawMessage) (plugineffects.Result, error) {
	update, err := plugineffects.DecodeContentUpdate(raw)
	if err != nil {
		return plugineffects.Result{}, plugineffects.ErrInvalid
	}
	manifest, err := json.Marshal(control.Manifest)
	if err != nil {
		return plugineffects.Result{}, plugineffects.ErrInvalid
	}
	request := plugineffects.Request{Authorization: plugineffects.Authorization{Claim: invocation.claim,
		ManifestDigest: plugineffects.Digest(manifest), ModuleDigest: control.ModuleDigest}, Slot: slot, PostID: postID, Update: update}
	if _, _, err = request.Identity(); err != nil {
		return plugineffects.Result{}, err
	}
	return invocation.store.WriteContent(ctx, request)
}
