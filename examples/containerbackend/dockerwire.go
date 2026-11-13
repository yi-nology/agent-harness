// dockerwire.go —— Docker 实现的**传输管道**：端点、请求骨架与日志流 demux。
//
// 拆分只为满足单文件 400 行阈值（make architecture-check）：语义（自报什么、
// 执行步骤契约）在 docker.go，管道在这里。宿主改造接法时先读 docker.go。
package containerbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/oekit/pkg/errors"
)

// engineClient 按端点形态构造 HTTP 客户端（socket 走 DialContext）。
func engineClient(endpoint string) *http.Client {
	if rest, ok := strings.CutPrefix(endpoint, "unix://"); ok {
		return &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var nd net.Dialer
					return nd.DialContext(ctx, "unix", rest)
				},
			},
		}
	}
	return &http.Client{} // TCP / httptest 直连
}

// engineRequest 一次引擎调用的骨架：构造 URL、编码请求体、判定状态码。
func (d *Docker) engineRequest(ctx context.Context, method, path string, body any, query url.Values) (*http.Response, error) {
	u := d.engineURL(path, query)
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, errors.Wrap(ErrEngineCall, "containerbackend: 编码引擎请求失败: "+err.Error())
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, errors.Wrap(ErrEngineCall, "containerbackend: 构造引擎请求失败: "+err.Error())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(ErrEngineCall, "containerbackend: 引擎不可达: "+err.Error())
	}
	return resp, nil
}

// engineURL 拼引擎 URL（unix socket 时 Host 为占位名——socket 客户端忽略它）。
func (d *Docker) engineURL(path string, query url.Values) string {
	host := d.endpoint
	if strings.HasPrefix(d.endpoint, "unix://") {
		host = "http://d"
	}
	u := host + "/" + d.apiVer + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// expectOK 引擎非 2xx 即错（错误体带引擎自己的 message）。
func expectOK(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	return errors.Wrap(ErrEngineCall, fmt.Sprintf("containerbackend: 引擎返回 %s: %s", resp.Status, strings.TrimSpace(string(msg))))
}

// demuxLogs 把引擎多路复用日志流拆成 stdout / stderr 两路。
//
// 帧形态（Docker Engine API 附加协议）：8 字节头 = [stream, 0, 0, 0,
// size(4 字节大端)]，随后 size 字节负载。缺帧头（TTY 形态）整段当 stdout。
func demuxLogs(raw []byte) (string, string) {
	var (
		out [2]strings.Builder
		pos int
	)
	for pos+8 <= len(raw) {
		stream := raw[pos]
		size := int(raw[pos+4])<<24 | int(raw[pos+5])<<16 | int(raw[pos+6])<<8 | int(raw[pos+7])
		pos += 8
		if pos+size > len(raw) { // 半截帧：不装作完整，丢弃
			break
		}
		idx := 0
		if stream == 2 {
			idx = 1
		}
		out[idx].Write(raw[pos : pos+size])
		pos += size
	}
	if pos == 0 && len(raw) > 0 { // 无帧头 = TTY 流，整段按 stdout
		out[0].Write(raw)
	}
	return out[0].String(), out[1].String()
}
