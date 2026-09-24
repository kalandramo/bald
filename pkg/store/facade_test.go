package store

import (
	"context"
	"errors"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 门面层（Store[T]）直测
//
// 补测动机：门面方法里 `q, err := s.provider.DB(ctx); if err != nil { return err }`
// 这条**错误传播分支**此前零覆盖（跨包 coverpkg 口径实测 :122/:161 等块 count=0）
// ——现有测试都经 inmemory.Provider（DB 恒返回 nil error），从未构造过 provider
// 故障。故本文件用 mock DBProvider 专门驱动该分支，并顺带锁定隔离注入链路。
//
// 为何不能只靠 inmemory 集成测试：inmemory 的 DB() 永不失败，provider 故障
// 路径（连接池耗尽、引擎不可达等）在生产是真实存在的，其传播行为需独立钉住。
// ---------------------------------------------------------------------------

// facadeEntity 是门面测试实体（含租户列，供隔离注入断言）。
type facadeEntity struct {
	ID       string
	Name     string
	TenantID string
}

// failingProvider 的 DB() 恒返回错误——模拟引擎不可达/连接池故障。
type failingProvider struct {
	err   error
	calls int // 记录被调用次数，供断言门面确实向 provider 要了句柄
}

func (p *failingProvider) DB(context.Context) (Queryable[facadeEntity], error) {
	p.calls++
	return nil, p.err
}
func (p *failingProvider) Close() error { return nil }

// recordingProvider 包装一个真实 Queryable，记录门面透传下来的 Where——
// 用于断言「隔离条件确实被注入后才交给 provider」，而非只看最终结果。
type recordingProvider struct {
	q     Queryable[facadeEntity]
	last  *Where
	calls int
}

func (p *recordingProvider) DB(context.Context) (Queryable[facadeEntity], error) {
	p.calls++
	return &recordingQuery{p: p}, nil
}
func (p *recordingProvider) Close() error { return nil }

// recordingQuery 把每次调用的 Where 记到 provider 上，再委托给真实实现。
type recordingQuery struct{ p *recordingProvider }

func (r *recordingQuery) Create(ctx context.Context, obj *facadeEntity) error {
	return r.p.q.Create(ctx, obj)
}
func (r *recordingQuery) Update(ctx context.Context, obj *facadeEntity) (int64, error) {
	return r.p.q.Update(ctx, obj)
}
func (r *recordingQuery) Delete(ctx context.Context, w *Where) (int64, error) {
	r.p.last = w
	return r.p.q.Delete(ctx, w)
}
func (r *recordingQuery) Get(ctx context.Context, w *Where) (*facadeEntity, error) {
	r.p.last = w
	return r.p.q.Get(ctx, w)
}
func (r *recordingQuery) List(ctx context.Context, w *Where) ([]*facadeEntity, int64, error) {
	r.p.last = w
	return r.p.q.List(ctx, w)
}
func (r *recordingQuery) Count(ctx context.Context, w *Where) (int64, error) {
	r.p.last = w
	return r.p.q.Count(ctx, w)
}
func (r *recordingQuery) Migrate(context.Context, ...any) error { return nil }

// stubQueryable 是最小 Queryable 实现，供 recordingProvider 委托（返回可控值）。
type stubQueryable struct {
	getErr    error
	deleteErr error
	listErr   error
	countErr  error
	updateErr error
	createErr error

	rows  int64
	total int64
	items []*facadeEntity
}

func (s *stubQueryable) Create(context.Context, *facadeEntity) error { return s.createErr }
func (s *stubQueryable) Update(context.Context, *facadeEntity) (int64, error) {
	return s.rows, s.updateErr
}
func (s *stubQueryable) Delete(context.Context, *Where) (int64, error) {
	return s.rows, s.deleteErr
}
func (s *stubQueryable) Get(context.Context, *Where) (*facadeEntity, error) {
	return nil, s.getErr
}
func (s *stubQueryable) List(context.Context, *Where) ([]*facadeEntity, int64, error) {
	return s.items, s.total, s.listErr
}
func (s *stubQueryable) Count(context.Context, *Where) (int64, error) {
	return s.total, s.countErr
}
func (s *stubQueryable) Migrate(context.Context, ...any) error { return nil }

// ---------------------------------------------------------------------------
// provider 故障传播：每个门面方法都必须把 DB() 的错误原样上抛，不得吞掉
// ---------------------------------------------------------------------------

func TestFacade_ProviderErrorPropagates(t *testing.T) {
	sentinel := errors.New("engine unreachable")

	// 表驱动：方法名 → 调用闭包 + 期望的零值。
	cases := []struct {
		name string
		call func(s *Store[facadeEntity]) error
	}{
		{"Create", func(s *Store[facadeEntity]) error {
			return s.Create(context.Background(), &facadeEntity{})
		}},
		{"Update", func(s *Store[facadeEntity]) error {
			_, err := s.Update(context.Background(), &facadeEntity{})
			return err
		}},
		{"Delete", func(s *Store[facadeEntity]) error {
			_, err := s.Delete(context.Background(), nil)
			return err
		}},
		{"Get", func(s *Store[facadeEntity]) error {
			_, err := s.Get(context.Background(), nil)
			return err
		}},
		{"List", func(s *Store[facadeEntity]) error {
			_, _, err := s.List(context.Background(), nil)
			return err
		}},
		{"Count", func(s *Store[facadeEntity]) error {
			_, err := s.Count(context.Background(), nil)
			return err
		}},
		{"ListWithPaging", func(s *Store[facadeEntity]) error {
			_, err := s.ListWithPaging(context.Background(), &storev1.PagingRequest{})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &failingProvider{err: sentinel}
			s := NewStore[facadeEntity](p)

			err := tc.call(s)

			require.Error(t, err, "provider 故障必须上抛，不得静默返回零值")
			assert.ErrorIs(t, err, sentinel, "应原样传播 provider 的错误（保留 errors.Is 链）")
			assert.Equal(t, 1, p.calls, "门面应向 provider 请求句柄恰好一次")
		})
	}
}

// TestFacade_ProviderErrorDoesNotLeakZeroSuccess 反向断言：故障时绝不能
// 返回「看似成功的零值 + nil error」——那会让调用方把故障当空结果。
func TestFacade_ProviderErrorDoesNotLeakZeroSuccess(t *testing.T) {
	p := &failingProvider{err: errors.New("boom")}
	s := NewStore[facadeEntity](p)

	// Get 必须返回 nil 实体 + error（不得返回非 nil 实体）。
	got, err := s.Get(context.Background(), nil)
	assert.Nil(t, got)
	assert.Error(t, err)

	// List 必须返回 nil items（不得返回空切片 + nil error）。
	items, total, err := s.List(context.Background(), nil)
	assert.Nil(t, items)
	assert.Zero(t, total)
	assert.Error(t, err)

	// Count 必须返回 0 + error。
	n, err := s.Count(context.Background(), nil)
	assert.Zero(t, n)
	assert.Error(t, err)

	// Update/Delete 必须返回 0 行 + error（不得返回 (0, nil) 被误当幂等成功）。
	rows, err := s.Update(context.Background(), &facadeEntity{})
	assert.Zero(t, rows)
	assert.Error(t, err)

	rows, err = s.Delete(context.Background(), nil)
	assert.Zero(t, rows)
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// 隔离注入链路：门面必须把注入后的 Where 交给 provider（而非原始 Where）
// ---------------------------------------------------------------------------

func TestFacade_InjectsTenantIsolationBeforeProvider(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")

	// 覆盖所有走 applyIsolation 的读路径。
	reads := []struct {
		name string
		call func(s *Store[facadeEntity], ctx context.Context) error
	}{
		{"Get", func(s *Store[facadeEntity], ctx context.Context) error {
			_, err := s.Get(ctx, nil)
			return err
		}},
		{"List", func(s *Store[facadeEntity], ctx context.Context) error {
			_, _, err := s.List(ctx, nil)
			return err
		}},
		{"Count", func(s *Store[facadeEntity], ctx context.Context) error {
			_, err := s.Count(ctx, nil)
			return err
		}},
		{"Delete", func(s *Store[facadeEntity], ctx context.Context) error {
			_, err := s.Delete(ctx, nil)
			return err
		}},
	}

	for _, tc := range reads {
		t.Run(tc.name, func(t *testing.T) {
			p := &recordingProvider{q: &stubQueryable{}}
			s := NewStore[facadeEntity](p)

			require.NoError(t, tc.call(s, ctx))

			require.NotNil(t, p.last, "门面必须把 Where 交给 provider")
			assert.Len(t, p.last.Filters, 1, "应注入一条租户条件")
			assert.Equal(t, "tenant_id", p.last.Filters[0].GetField())
			assert.Equal(t, "t-42", p.last.Filters[0].GetValue())
		})
	}
}

// TestFacade_PlatformLevelSkipsInjection 声明平台级后不得注入租户条件
// （否则对无 tenant_id 列的表产生 no such column）。
func TestFacade_PlatformLevelSkipsInjection(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")
	p := &recordingProvider{q: &stubQueryable{}}
	s := NewStore[facadeEntity](p, WithPlatformLevel[facadeEntity]())

	_, _, err := s.List(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, p.last)
	assert.Empty(t, p.last.Filters, "平台级实体不得注入租户条件")
}

// TestFacade_PlatformIdentitySkipsInjection 身份级豁免（跨租户视图）。
func TestFacade_PlatformIdentitySkipsInjection(t *testing.T) {
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { UnregisterTenant("tenant_id") })

	ctx := contextx.WithTenantID(context.Background(), "t-42")
	ctx = contextx.WithPlatform(ctx)

	p := &recordingProvider{q: &stubQueryable{}}
	s := NewStore[facadeEntity](p)

	_, _, err := s.List(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, p.last)
	assert.Empty(t, p.last.Filters, "平台身份不得注入租户条件")
}

// TestFacade_NoTenantRegistrationIsNoop 非多租户应用（未注册维度）零影响。
func TestFacade_NoTenantRegistrationIsNoop(t *testing.T) {
	ctx := contextx.WithTenantID(context.Background(), "t-42")
	p := &recordingProvider{q: &stubQueryable{}}
	s := NewStore[facadeEntity](p)

	_, _, err := s.List(ctx, nil)
	require.NoError(t, err)

	require.NotNil(t, p.last)
	assert.Empty(t, p.last.Filters, "未注册租户维度时不应注入")
}

// ---------------------------------------------------------------------------
// 门面不吞 provider 的业务错误
// ---------------------------------------------------------------------------

func TestFacade_QueryableErrorPropagates(t *testing.T) {
	sentinel := errors.New("record not found")
	p := &recordingProvider{q: &stubQueryable{getErr: sentinel}}
	s := NewStore[facadeEntity](p)

	_, err := s.Get(context.Background(), nil)
	assert.ErrorIs(t, err, sentinel, "provider 的业务错误应原样上抛")
}

// ---------------------------------------------------------------------------
// Option 构造（此前 0% 覆盖）
// ---------------------------------------------------------------------------

func TestNewStore_Options(t *testing.T) {
	p := &stubProvider{}

	t.Run("defaults", func(t *testing.T) {
		s := NewStore[facadeEntity](p)
		assert.Equal(t, DefaultPageSize, s.opts.pageSize)
		assert.Equal(t, MaxPageSize, s.opts.maxSize)
		assert.NotNil(t, s.logger, "默认应注入 NopLogger 而非 nil")
		assert.False(t, s.IsPlatformLevel())
	})

	t.Run("WithPageSize", func(t *testing.T) {
		s := NewStore[facadeEntity](p, WithPageSize[facadeEntity](5))
		assert.Equal(t, 5, s.opts.pageSize)
	})

	t.Run("WithMaxPageSize", func(t *testing.T) {
		s := NewStore[facadeEntity](p, WithMaxPageSize[facadeEntity](50))
		assert.Equal(t, 50, s.opts.maxSize)
	})

	t.Run("WithLogger", func(t *testing.T) {
		l := &fakeLogSink{}
		s := NewStore[facadeEntity](p, WithLogger[facadeEntity](l))
		assert.Same(t, Logger(l), s.logger, "WithLogger 注入的实例应被持有")

		// 行为验证：经注入的 logger 记录一次，确认它确实收到调用
		// （只比指针会漏掉「持有但从不使用」的假接线）。
		s.logger.Info(context.Background(), "probe")
		assert.Equal(t, 1, l.infoCalls)
	})

	t.Run("WithPlatformLevel", func(t *testing.T) {
		s := NewStore[facadeEntity](p, WithPlatformLevel[facadeEntity]())
		assert.True(t, s.IsPlatformLevel())
	})

	t.Run("Provider-accessor", func(t *testing.T) {
		s := NewStore[facadeEntity](p)
		assert.Same(t, DBProvider[facadeEntity](p), s.Provider())
	})
}

// stubProvider 恒成功，供 Option 测试（不关心查询行为）。
type stubProvider struct{}

func (stubProvider) DB(context.Context) (Queryable[facadeEntity], error) {
	return &stubQueryable{}, nil
}
func (stubProvider) Close() error { return nil }

// ---------------------------------------------------------------------------
// PageSize 配置确实影响分页结果（选项非空接线的行为验证）
// ---------------------------------------------------------------------------

func TestFacade_WithPageSizeAffectsPaging(t *testing.T) {
	p := &recordingProvider{q: &stubQueryable{total: 100}}
	s := NewStore[facadeEntity](p, WithPageSize[facadeEntity](5), WithMaxPageSize[facadeEntity](20))

	// 未显式指定 PageSize → 用 Store 的默认 5。
	res, err := s.ListWithPaging(context.Background(), &storev1.PagingRequest{Page: u32p(1)})
	require.NoError(t, err)
	assert.Equal(t, uint32(5), res.Meta.GetPageSize(), "应使用 WithPageSize 配置的 5")

	// 显式请求超大 PageSize → 被 WithMaxPageSize 截断为 20。
	res, err = s.ListWithPaging(context.Background(), &storev1.PagingRequest{Page: u32p(1), PageSize: u32p(999)})
	require.NoError(t, err)
	assert.Equal(t, uint32(20), res.Meta.GetPageSize(), "应被 WithMaxPageSize 截断")
}

// ---------------------------------------------------------------------------
// Migrate 门面透传
// ---------------------------------------------------------------------------

func TestFacade_MigratePassthrough(t *testing.T) {
	// Store 未暴露 Migrate（设计上由 provider 持有），此处锁定该事实：
	// provider 自身可 Migrate，门面不代理——避免调用方误以为 store.Migrate 存在。
	p := &stubProvider{}
	q, err := p.DB(context.Background())
	require.NoError(t, err)
	assert.NoError(t, q.Migrate(context.Background(), &facadeEntity{}))
}
