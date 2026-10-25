package protocol

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/oekit/pkg/cache"
	"github.com/oekit/pkg/errors"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaCache 编译缓存：键=schema JSON 原文。声明单源在 tools/serverkit 注册表
// （registry.go），常量串即缓存键，天然按内容去重；编译失败不缓存（常量已被
// serverkit/各 server 包内 conformance 测试验证，实践不可达）。
//
// 单飞去重：并发首次编译同一 schema 时只编译一次，其余请求共享结果——
// JSON Schema 编译是本文件最重的一步，合并并发编译收益直接。
//
// TTL=0 ⇒ 永不过期，且不引入后台清扫 goroutine（与原 sync.Map 无常驻协程一致）。
var schemaCache = cache.NewSingleFlightCache(cache.NewLocalCache[jsonschema.Schema](cache.LocalConfig[jsonschema.Schema]{
	NullTTL: -1, // 编译失败走 err 分支本就不写 null marker；显式禁用空值缓存，杜绝「查不到」被误当缓存事实
}))

// ValidateStructured 客户端 SHOULD 校验（MCP 2025-06-18，批次一百一十一）：工具
// 的 structuredContent 对照其声明 outputSchema。仅对注册表在册工具生效（schemaSrc
// 空 = 未声明，合法态直接放行）；信封无 structuredContent（旧 server/纯文本工具）
// 同样放行——双写兼容语义。校验失败=服务端漂移信号：返回 error 交调用方可视化
// （metrics/事件），不阻断主流程——text 镜像谓词兜底仍成立。
func ValidateStructured(schemaSrc, envelope string) error {
	if schemaSrc == "" {
		return nil
	}
	sp, ok := StructuredPayload(envelope)
	if !ok {
		return nil
	}
	sch, err := compileSchema(schemaSrc)
	if err != nil {
		return errors.Wrap(err, "outputSchema 编译失败")
	}
	var v any
	if err := json.Unmarshal([]byte(sp), &v); err != nil {
		return errors.Wrap(err, "structuredContent 非 JSON")
	}
	if err := sch.Validate(v); err != nil {
		return errors.Wrap(err, "structuredContent 违反声明 outputSchema")
	}
	return nil
}

func compileSchema(schemaSrc string) (*jsonschema.Schema, error) {
	return schemaCache.GetOrLoadWithSource(context.Background(), schemaSrc, func(_ context.Context, src string) (*jsonschema.Schema, error) {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
		if err != nil {
			return nil, err
		}
		comp := jsonschema.NewCompiler()
		if err := comp.AddResource("schema.json", doc); err != nil {
			return nil, err
		}
		sch, err := comp.Compile("schema.json")
		if err != nil {
			return nil, err
		}
		return sch, nil
	})
}
