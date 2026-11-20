package backend

import (
	"context"
	"fmt"
	"time"
)

func chatGPTCDP(ctx context.Context, target chatGPTPageTarget) (func(string, map[string]any) (map[string]any, error), func(), error) {
	conn, err := cdpDialWebSocket(target.Socket)
	if err != nil {
		return nil, nil, err
	}
	conn.SetReadLimit(2 << 20)
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(75 * time.Second)
	}
	conn.SetReadDeadline(deadline)
	conn.SetWriteDeadline(deadline)
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	id := 0
	call := func(method string, params map[string]any) (map[string]any, error) {
		id++
		if err := conn.WriteJSON(cdpMessage{Id: id, Method: method, Params: params}); err != nil {
			return nil, err
		}
		for {
			var response cdpResponse
			if err := conn.ReadJSON(&response); err != nil {
				return nil, err
			}
			if response.Id != id {
				continue
			}
			if response.Error != nil {
				return nil, fmt.Errorf("浏览器查询失败：%s", response.Error.Message)
			}
			if response.Result["exceptionDetails"] != nil {
				return nil, fmt.Errorf("ChatGPT 页面查询中断，请确认页面正常后重试")
			}
			return response.Result, nil
		}
	}
	return call, func() { stopCancel(); _ = conn.Close() }, nil
}
