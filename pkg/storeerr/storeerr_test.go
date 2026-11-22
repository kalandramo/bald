package storeerr_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/store"
	"github.com/kalandramo/bald/pkg/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToBError_Sentinels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   uint32
		wantReason string
	}{
		{"ErrNotFound", store.ErrNotFound, berrors.CodeNotFound, "USER_NOT_FOUND"},
		{"ErrConflict", store.ErrConflict, berrors.CodeAlreadyExists, "USER_CONFLICT"},
		{"ErrInvalidToken", store.ErrInvalidToken, berrors.CodeInvalidArgument, "BAD_TOKEN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := storeerr.ToBError(tt.err, tt.wantReason)
			require.True(t, ok, "哨兵应被识别")
			require.NotNil(t, got)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantReason, got.Reason)
		})
	}
}

func TestToBError_WrappedStillRecognized(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", store.ErrNotFound)
	got, ok := storeerr.ToBError(wrapped, "ORDER_NOT_FOUND")
	require.True(t, ok, "%w 包装的哨兵仍应被识别")
	require.NotNil(t, got)
	assert.Equal(t, berrors.CodeNotFound, got.Code)
	assert.Equal(t, "ORDER_NOT_FOUND", got.Reason)
}

func TestToBError_NonSentinelAndNil(t *testing.T) {
	// 普通错误不被识别。
	got, ok := storeerr.ToBError(errors.New("plain error"), "WHATEVER")
	assert.False(t, ok)
	assert.Nil(t, got)

	// nil 不被识别。
	got, ok = storeerr.ToBError(nil, "WHATEVER")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestToBError_AlreadyBErrorNotRewrapped(t *testing.T) {
	// 已是 *berrors.Error 的错误不应被二次包装（返回 false）。
	orig := berrors.NotFound("ALREADY")
	got, ok := storeerr.ToBError(orig, "SHOULD_NOT_APPLY")
	assert.False(t, ok)
	assert.Nil(t, got)
}
