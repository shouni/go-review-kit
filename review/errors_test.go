package review

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrapStep(t *testing.T) {
	t.Run("nil はそのまま nil", func(t *testing.T) {
		if err := WrapStep(StepDiff, nil); err != nil {
			t.Fatalf("nil を期待しましたが: %v", err)
		}
	})

	t.Run("工程名を付けても errors.Is が通る", func(t *testing.T) {
		err := WrapStep(StepDiff, fmt.Errorf("解決できません: %w", ErrRefNotFound))

		if !errors.Is(err, ErrRefNotFound) {
			t.Fatalf("番兵エラーまで辿れません: %v", err)
		}
		if got := StepOf(err); got != StepDiff {
			t.Fatalf("StepOf = %q, want %q", got, StepDiff)
		}
	})

	t.Run("さらに包んでも工程名を取り出せる", func(t *testing.T) {
		err := fmt.Errorf("パイプライン: %w", WrapStep(StepPublish, errors.New("書き込み失敗")))

		if got := StepOf(err); got != StepPublish {
			t.Fatalf("StepOf = %q, want %q", got, StepPublish)
		}
	})
}

func TestStepOfWithoutStep(t *testing.T) {
	if got := StepOf(errors.New("素のエラー")); got != "" {
		t.Fatalf("StepOf = %q, want 空文字", got)
	}
	if got := StepOf(nil); got != "" {
		t.Fatalf("StepOf(nil) = %q, want 空文字", got)
	}
}

func TestStepErrorMessage(t *testing.T) {
	err := WrapStep(StepPrepare, errors.New("クローンに失敗"))

	want := "リポジトリの準備 に失敗しました: クローンに失敗"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}
