// record.go —— Store 适配器的记录形态与三项联查。
// 从 store.go 拆出（结构阈值按判据的一侧切）：两份状态（Key 级 act /
// 资源级 res）的落库形态、编解码，以及「令牌是最新代 + 持有者是授予铸造
// 的那份 + 租约与动作资源一致」的联查口径——判定入口在 store.go，
// 它们消费的凭据语义在这里。
package coord

import (
	"encoding/json"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// ── 记录形态 ──────────────────────────────────────────────────────────────

// actRec Key 级状态（一个逻辑动作的全部投影）。
type actRec struct {
	Done      bool       `json:"done,omitempty"`
	Result    []byte     `json:"result,omitempty"`
	Uncertain bool       `json:"uncertain,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Intent    *intentRec `json:"intent,omitempty"`
	Recon     *reconRec  `json:"recon,omitempty"`

	// 动作身份（首次登记，永久事实；IDSet=false = 未登记）。与
	// sideeffect.ErrActionConflict 对应：Key 声明「这是同一个逻辑动作」，
	// 但 Resource / Payload / RetrySafety 才是动作的定义——三者可被后续
	// 调用改写时，「不可重试」会被后到的 Safe 声明推翻（重复施加的入口）。
	IDSet         bool                   `json:"idSet,omitempty"`
	IDResource    string                 `json:"idResource,omitempty"`
	IDSafety      sideeffect.RetrySafety `json:"idSafety,omitempty"`
	IDPayloadHash string                 `json:"idPayloadHash,omitempty"`
}

// intentRec 一次未裁决的执行意图（Acquire 原子登记）。
type intentRec struct {
	Resource string    `json:"res"`
	Fence    uint64    `json:"fence"`
	Holder   string    `json:"holder"`
	Expires  time.Time `json:"expires"`
}

// reconRec 一次对账认领。
type reconRec struct {
	Holder  string    `json:"holder"`
	Expires time.Time `json:"expires"`
}

// resRec 资源级状态（独占持有 + 栅栏令牌）。
type resRec struct {
	Fence  uint64    `json:"fence"`
	Holder string    `json:"holder,omitempty"`
	Until  time.Time `json:"until,omitempty"`
}

// decodeAct / decodeRes 解码（内容不是本适配器的编码 ⇒ ErrMalformedRecord）。
func decodeAct(e Entry) (actRec, error) {
	var r actRec
	if !e.Exists {
		return r, nil
	}
	if err := json.Unmarshal(e.Val, &r); err != nil {
		return actRec{}, errors.Wrap(ErrMalformedRecord, "动作记录解码失败", "key", e.Key)
	}
	return r, nil
}

func decodeRes(e Entry) (resRec, error) {
	var r resRec
	if !e.Exists {
		return r, nil
	}
	if err := json.Unmarshal(e.Val, &r); err != nil {
		return resRec{}, errors.Wrap(ErrMalformedRecord, "资源记录解码失败", "key", e.Key)
	}
	return r, nil
}

// ── 写入构造件 ────────────────────────────────────────────────────────────

// actMutation 编码整份 act 记录为一次写入。
func actMutation(key sideeffect.Key, a actRec) (Mutation, error) {
	enc, err := json.Marshal(a)
	if err != nil {
		return Mutation{}, errors.Wrap(err, "coord: 编码动作记录失败", "key", string(key))
	}
	return Mutation{Key: actKeyPrefix + string(key), Val: enc}, nil
}

// resMutation 编码整份 res 记录为一次写入。
func resMutation(resource string, rr resRec) (Mutation, error) {
	enc, err := json.Marshal(rr)
	if err != nil {
		return Mutation{}, errors.Wrap(err, "coord: 编码资源记录失败", "resource", resource)
	}
	return Mutation{Key: resKeyPrefix + resource, Val: enc}, nil
}

// ── 三项联查（与参考实现同口径：判定顺序有意如此）────────────────────────

// holdOwnerErr 资源闸是否仍由**这份凭据**持有。
// 令牌不是最新代或资源已释放 → ErrFenceStale；令牌还在这一代但持有者
// 不是出示者 → ErrNotHolder（令牌会经下游流转，见过它的组件拿着令牌
// 冒充持有者，被这道防线拦下）。
func holdOwnerErr(act sideeffect.Action, lease sideeffect.Lease, rr resRec) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if lease.Fence == 0 || rr.Holder == "" || rr.Fence != uint64(lease.Fence) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次持有已失效（令牌不是最新代或资源已释放）",
			"key", string(act.Key), "resource", act.Resource)
	}
	if rr.Holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（令牌不是本次授予铸造的那份）",
			"key", string(act.Key), "resource", act.Resource)
	}
	return nil
}

// intentOwnerErr 本次尝试是否仍归**这份凭据**所有。
func intentOwnerErr(act sideeffect.Action, lease sideeffect.Lease, a actRec) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if a.Intent == nil {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已无未裁决的执行意图", "key", string(act.Key))
	}
	if a.Intent.Fence != uint64(lease.Fence) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	if a.Intent.Holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（意图不是本次授予铸造的那份）",
			"key", string(act.Key))
	}
	return nil
}

// leaseResourceErr 租约的资源必须与动作的资源一致（A 资源的租约被拿去做
// B 资源的操作时，两份真相拼不出一个合规）。
func leaseResourceErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if lease.Resource != act.Resource {
		return errors.Wrap(sideeffect.ErrResourceMismatch,
			"凭据被拒：租约与动作的资源不一致",
			"act_resource", act.Resource, "lease_resource", lease.Resource)
	}
	return nil
}

// ── 动作身份不可变（评审第三轮 P0，issue #137）────────────────────────────

// registerIdentity 首次见到该 Key 时登记动作身份；返回是否登记了新身份
// （调用方把它计进 dirty——登记也是一次写入，不得悬在判定里）。
func registerIdentity(a *actRec, act sideeffect.Action) bool {
	if a.IDSet {
		return false
	}
	a.IDSet = true
	a.IDResource = act.Resource
	a.IDSafety = act.RetrySafety
	a.IDPayloadHash = sideeffect.HashPayload(act.Payload)
	return true
}

// identityErr 报告本次动作的身份与首次登记是否一致。
//
// 身份判定优先于一切状态判定（Store.Acquire 契约）：不授予、不重放既有
// 结果、不转未知态——把旧身份的结果交给新身份的调用方就是答非所问，
// 把「不可重试」交给后到的 Safe 声明就是重复施加。
// 记录未登记身份（旧记录 / 空记录）视为无冲突——授予路径会把它登记进去。
func identityErr(a actRec, act sideeffect.Action) error {
	if !a.IDSet {
		return nil
	}
	if a.IDResource != act.Resource || a.IDSafety != act.RetrySafety ||
		a.IDPayloadHash != sideeffect.HashPayload(act.Payload) {
		return errors.Wrap(sideeffect.ErrActionConflict,
			"同 Key 的动作身份与首次登记不一致（这不是重试，是另一个动作）",
			"key", string(act.Key),
			"登记 resource", a.IDResource, "本次 resource", act.Resource,
			"登记 safety", a.IDSafety, "本次 safety", act.RetrySafety)
	}
	return nil
}
