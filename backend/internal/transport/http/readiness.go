package httpserver

import (
	"context"
	"time"

	"github.com/chenyme/grok2api/backend/internal/pkg/resultcache"
)

const (
	// readinessCacheTTL 限制匿名 /readyz 触发运行态存储、路由与候选账号扫描的频率。
	readinessCacheTTL = 5 * time.Second
	// readinessComputeTimeout 约束一次共享计算；计算与首个调用方的生命周期解耦，
	// 避免断开的探针把被取消的结果写入缓存。
	readinessComputeTimeout = 10 * time.Second
)

// readinessProbe 让并发探针共享同一次就绪计算，并在 readinessCacheTTL 内复用结果。
type readinessProbe struct {
	compute func(context.Context) ReadinessSnapshot
	cache   *resultcache.Cache[struct{}, ReadinessSnapshot]
	now     func() time.Time
}

func newReadinessProbe(compute func(context.Context) ReadinessSnapshot) *readinessProbe {
	if compute == nil {
		return nil
	}
	return &readinessProbe{compute: compute, cache: resultcache.New[struct{}, ReadinessSnapshot](1, readinessCacheTTL), now: time.Now}
}

func (p *readinessProbe) snapshot(ctx context.Context) (ReadinessSnapshot, error) {
	return p.cache.Load(ctx, struct{}{}, p.now(), func() (ReadinessSnapshot, error) {
		computeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readinessComputeTimeout)
		defer cancel()
		return p.compute(computeCtx), nil
	})
}

// publicReadiness 是匿名 /readyz 的响应：只保留总体与各组件状态，不含自由文本详情和启动统计。
func publicReadiness(snapshot ReadinessSnapshot) ReadinessSnapshot {
	public := ReadinessSnapshot{Ready: snapshot.Ready, State: snapshot.State, UpdatedAt: snapshot.UpdatedAt}
	if len(snapshot.Components) > 0 {
		public.Components = make(map[string]ReadinessComponent, len(snapshot.Components))
		for name, component := range snapshot.Components {
			public.Components[name] = ReadinessComponent{State: component.State}
		}
	}
	return public
}
