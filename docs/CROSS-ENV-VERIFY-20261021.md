# 跨环境验证：有些路径本机跑不了，怎么验 (2026-10-21)

> 一句话：**「门禁全绿」不等于「机制成立」——尤其当机制依赖一个本机没有的环境。**
>
> 本篇记录 2026-10-21 一次真实的翻车与修法：**内存兜底（cgroup v2）在 macOS
> 与 CI 上都跑不了，而 CI 是绿的**。

---

## 一、那次翻车：CI 绿了，但核心验收是 SKIP

`ptc` 的子进程后端承诺「内存兜底」——不可信程序撑爆物理内存时，内核 OOM-killer
杀掉子进程，**宿主进程存活**。这条是「不兜住的形态是『一个不可信程序能杀掉宿主』」
的封口。

改动做完后：

- 本机（macOS）`make check` 全绿；
- 推上去，CI（ubuntu-24.04）**也全绿**。

看起来没问题。但**那条端到端验收在 CI 上是 SKIP，不是 PASS**。

### 怎么发现的

不是靠读 CI 日志（`make check` 不加 `-v`，日志里没有测试名），
而是**主动去问 CI 环境到底有什么能力**——临时加一个 workflow 探测：

```yaml
- name: 探测 cgroup v2 与 memory 控制器
  run: |
    cat /sys/fs/cgroup/cgroup.controllers      # → cpuset cpu io memory hugetlb pids rdma misc dmem
    mkdir -p /sys/fs/cgroup/probe-$$            # → Permission denied !!
    OWN=$(awk -F: '$1=="0"{print $3}' /proc/self/cgroup)
    mkdir -p /sys/fs/cgroup$OWN/probe-$$        # → Permission denied !!
```

结果：

```
--- cgroup.controllers ---
cpuset cpu io memory hugetlb pids rdma misc dmem   ← memory 控制器「可见」
--- 统一层级根下建子组 ---
mkdir: cannot create directory '/sys/fs/cgroup/probe-2489': Permission denied
--- own=/system.slice/hosted-compute-agent.service ---
own-mkdir FAILED
--- SKIP: TestSubprocessMemoryLimitKillsChild (0.00s)
```

**「控制器可见」≠「能建子组」**。GitHub 的 runner 是容器化的，两条候选路径
都建不出来 ⇒ 机制**从未被执行过**，而门禁显示绿。

### 教训（可复用）

1. **门禁绿必须区分「跑过且通过」与「跳过」**。带 `t.Skip` 的用例在
   `make check` 的输出里与真跑通过**长得一样**（都是 `ok`）。
   ⇒ **平台的可用性断言要有单独的探针**，不能靠「用例绿」推断「环境对」。
2. **「某能力可见」与「该能力可用」是两件事**。`cgroup.controllers` 里有
   `memory` 只说明内核认得它；能不能建子 cgroup 是**权限**问题。
3. **平台的可用性断言要有单独的探针**，不能靠「用例绿」推断「环境对」。

---

## 二、正确的验法：找一个真能跑的环境

**判据**：找一台**能建出子 cgroup 的 Linux 机器**，把测试二进制**交叉编译**过去跑。

本轮用的是 `bridge-work`（Ubuntu，root）。实测：

```
--- 统一层级根下建子组 ---
统一层级根下 mkdir OK
memory.max 存在
写 memory.max OK
内容: 268435456
--- own 之下 ---
own 下 mkdir OK
无 memory.max            ← 「自己的 cgroup 之下」建得出但没有 memory.max
```

**注意第二个发现**：候选路径「进程自己的 cgroup 之下」在这台机器上**建得出
但没有 memory.max**（那是个 systemd slice，内存控制器没委派下去）。
⇒ 实现**必须逐个候选试**，且「建得出」不等于「可用」。

### 交叉编译跑测试（目标机无 Go 工具链）

测试要现场编译一个子进程程序（`ptc.MaybeRunChild()`），目标机没 Go 就跑不了。
解法：**让测试支持用预编译的子进程二进制**。

```go
// 测试侧：环境变量指定预编译子进程
const childBinEnv = "AGENT_HARNESS_PTC_CHILD_BIN"

func buildChildProgram(t *testing.T) string {
    if pre := os.Getenv(childBinEnv); pre != "" {
        return pre
    }
    // 否则现场编译（需要目标环境有 go）
}
```

编译与传输：

```bash
# 子进程程序（Linux 版）
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/child ./cmd/ptcchild/
# 测试二进制
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/ptc.test ./ptc/

scp /tmp/child /tmp/ptc.test root@<host>:/tmp/
ssh root@<host> 'cd /tmp && AGENT_HARNESS_PTC_CHILD_BIN=/tmp/child ./ptc.test -test.v'
```

### 结果

```
--- PASS: TestSubprocessMemoryLimitKillsChild (4.31s)     ← 真跑，非 SKIP
--- PASS: TestSubprocessProviderWithoutMemoryConfigured (0.00s)
--- PASS: TestMemoryLimitFailsClosedWhenCannotBuild (0.00s)
--- PASS: TestAttachFailureKillsChild (0.00s)
--- PASS: TestCgroupReleasedAfterExecution (0.00s)
--- PASS: TestNoCgroupWhenNoMemoryLimit (0.00s)
PASS
```

**并确认 cgroup 已回收干净**（残留的只有手工探测时建的 `ptc-probe-*`，
本仓建的是 `ptc-<pid>-<nano>` 格式且全部回收）——回收逻辑也被真验了。

### 机制本身也独立验过

不依赖本仓代码，直接在目标机上验 cgroup v2 的 memory.max：

```
memory.max = 128MB，attach 一个狂分配内存的子进程
→ 子进程被 Killed，退出码 137（128+9 = SIGKILL）
→ memory.events: oom 1 / oom_kill 1
```

**「被信号杀（退出码 -1 / 137）」正是 `ExecutionAborted.Signaled` 要识别的形态**
——机制与判定对得上。

---

## 三、由此产生的两条仓内改动

1. **测试支持预编译子进程**（`AGENT_HARNESS_PTC_CHILD_BIN`）：
   让同一份测试二进制能在**没有 Go 工具链的 Linux 机器**上跑——
   那正是唯一能建出子 cgroup 的环境形态。
2. **跳过必须写明理由**：`t.Skip` 的文案要写「本环境建不出带 memory.max 的
   cgroup（内存兜底不可用），该场景在此环境无法验证」，
   **不能让恒跳过的用例看起来像「测过了」**。

---

## 四、给下一轮的三条检查项

1. **新加一条平台相关机制时**：先问「它在 CI 上真跑还是跳过？」
   —— 用一个**探针 workflow** 验，别信绿灯。
2. **平台的可用性判据要写成「探测」而非「按平台猜」**：
   同一台 Linux 机器，换个容器跑（有没有委派 memory 控制器）结论就不同。
3. **跨环境验证要有可复现的路径**：交叉编译 + 预编译夹具 + 环境变量指定。
   本篇的命令可直接照抄。

---

## 五、附：本次实测到的环境矩阵

| 环境 | cgroup v2 | 能建子组 | 有 memory.max | 内存兜底可用 |
|---|---|---|---|---|
| 本机 macOS 27（arm64） | ❌ 无 | — | — | ❌ |
| 本机 Docker 容器（默认 cgroupns） | 骨架在 | ❌ 只读挂载 | — | ❌ |
| 本机 Docker 容器（`--cgroupns=private`） | ✅ | ❌ 只读挂载 | — | ❌ |
| GitHub Actions ubuntu-24.04 | ✅ | ❌ Permission denied | — | ❌ |
| bridge-work（Ubuntu，root，裸机） | ✅ | ✅ | ✅（仅统一层级根下） | ✅ |

**只有最后一格能验真兜底**。⇒ 这类机制的验收**不能只靠 CI**。
