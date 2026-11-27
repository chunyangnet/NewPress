package activity

import (
	"reflect"
	"testing"
)

func TestMentionsAreBoundedUsernamesNotEmail(t *testing.T) {
	got := Mentions("你好 @Reader @reader @用户甲；再问 (@other_name) 联系 mail@example.test 或 https://x.test/@hidden，不应触发。 @last-user")
	want := []string{"reader", "用户甲", "other_name", "last-user"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestMentionsAfterSentencePunctuation(t *testing.T) {
	got := Mentions("补充一条阅读笔记，@browser-reader 也欢迎来看看。再问：@用户甲；以及【@other_name】、\"@quoted\" 和 @first,@second。mail@example.test https://x.test/@hidden https://x.test/#@fragment")
	want := []string{"browser-reader", "用户甲", "other_name", "quoted", "first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
