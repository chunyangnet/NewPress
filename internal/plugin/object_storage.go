package plugin

const ObjectStorage = "object-storage"

func ObjectStorageManifest() Manifest {
	return Manifest{Schema: 1, Slug: ObjectStorage, Name: "对象存储", Version: "1.0.1", Description: "连接 S3、Cloudflare R2、腾讯 COS、阿里 OSS、七牛和 Cloudreve，提供媒体同步、校验和云端加速。", Category: "integration", Tags: []string{"对象存储", "CDN", "媒体同步", "Cloudreve"}, Author: "NewPress 官方", License: "Apache-2.0", NewPress: "builtin/object-storage-v1", Runtime: "builtin/object-storage-v1", Entry: "builtin.json", Capabilities: []string{"settings.read", "settings.write", "http.outbound", "media.storage"}}
}

func BundledManifests() []Manifest {
	return []Manifest{BuiltinManifest(), ObjectStorageManifest(), CDNManifest(), AIProMaxManifest()}
}

func IsBundledSlug(slug string) bool {
	return slug == CommentOrigin || slug == ObjectStorage || slug == CDN || slug == AIProMax
}
