package base

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"strings"
)

func (c *HttpClient) GetApiServer() string {
	return c.apiServer
}

func (c *HttpClient) GetApiKey() string {
	return c.apiKey
}

func (c *HttpClient) CreateBaseRequest(ctx context.Context, method, apiUrl string, body interface{}) (*http.Request, error) {
	if c.debug {
		log.Print("--== 请求URL:==--\n", fmt.Sprintf("%s %s", method, c.GetApiServer()+apiUrl))
	}

	var b io.Reader
	if body != nil {
		reqBytes, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		if c.debug {
			log.Print("--== 请求体:==--\n", string(reqBytes))
		}
		b = bytes.NewBuffer(reqBytes)
	} else {
		b = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, c.GetApiServer()+apiUrl, b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.GetApiKey())
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	return req, nil
}

func (c *HttpClient) CreateFormRequest(ctx context.Context, method, apiUrl string, data map[string]string) (*http.Request, error) {
	if c.debug {
		log.Print("--== 请求URL:==--\n", fmt.Sprintf("%s %s", method, c.GetApiServer()+apiUrl))
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// 添加表单字段
	for key, val := range data {
		err := writer.WriteField(key, val)
		if err != nil {
			return nil, err
		}
	}

	// 关闭writer
	writer.Close()

	req, err := http.NewRequestWithContext(ctx, method, c.GetApiServer()+apiUrl, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.GetApiKey())
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func (c *HttpClient) SendJSONRequest(req *http.Request, res interface{}) error {
	resp, err := c.SendRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if c.debug {
		// 使用 DumpResponse 获取响应内容，不影响原始 body
		dump, err := httputil.DumpResponse(resp, true)
		if err == nil {
			// 从 dump 中提取 JSON 部分
			parts := bytes.Split(dump, []byte("\r\n\r\n"))
			if len(parts) > 1 {
				// parts[0] 是 header，parts[1] 是 body
				var temp interface{}
				if err := json.Unmarshal(parts[1], &temp); err == nil {
					prettyJSON, err := json.MarshalIndent(temp, "", "    ")
					if err == nil {
						// 打印 header 和格式化后的 body
						log.Print("--== 响应头:==--\n", string(parts[0]))
						log.Print("--== 响应体:==--\n", string(prettyJSON))
					}
				} else {
					// 如果解析失败，就打印原始内容
					log.Print("response:", string(dump))
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errbody errBody
		err = json.NewDecoder(resp.Body).Decode(&errbody)
		if err != nil {
			return err
		}
		return fmt.Errorf("ERROR: %d [%v] %v", errbody.Status, errbody.Code, errbody.Message)
	}

	err = json.NewDecoder(resp.Body).Decode(res)
	if err != nil {
		return err
	}
	return nil
}

func (c *HttpClient) SendRawRequest(ctx context.Context, method, apiUrl string, req interface{}) (*http.Response, error) {
	httpReq, err := c.CreateBaseRequest(ctx, method, apiUrl, req)
	if err != nil {
		return nil, err
	}
	return c.SendRequest(httpReq)
}

func (c *HttpClient) SendRequest(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}

func (c *HttpClient) SSEEventHandle(ctx context.Context, resp *http.Response) (ch chan []byte) {
	ch = make(chan []byte)

	go func() {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()

		defer func() {
			resp.Body.Close()
			close(ch)
		}()

		reader := bufio.NewReader(resp.Body)
		var errBuffer strings.Builder

		for {
			select {
			case <-ctx.Done():
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						if c.debug {
							log.Println("接收SSE数据完成 io.EOF")
						}
						if errBuffer.Len() > 0 {
							handleErrorResponse(errBuffer.String(), ch)
						}
						return
					}
					ch <- []byte(fmt.Sprintf(errEventStr, "500", "read data err", err.Error()))
					return
				}

				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					if c.debug {
						var tmp interface{}
						if err := json.Unmarshal([]byte(data), &tmp); err == nil {
							prettyJSON, err := json.Marshal(tmp)
							if err == nil {
								log.Println("接收到SSE数据：", string(prettyJSON))
							} else {
								log.Println("接收到SSE数据：", data)
							}
						} else {
							log.Println("接收到SSE数据：", data)
						}
					}

					if len(data) == 0 {
						continue
					}

					ch <- []byte(data)
					continue
				}

				// ping的返回不是json字符串
				if line == "event: ping" {
					continue
				}

				errBuffer.WriteString(line)
			}
		}
	}()

	return
}
func handleErrorResponse(errStr string, ch chan []byte) {
	var errbody errBody
	err := json.Unmarshal([]byte(errStr), &errbody)
	if err == nil {
		ch <- []byte(fmt.Sprintf(errEventStr, errbody.Status, errbody.Code, errbody.Message))
	} else {
		ch <- []byte(fmt.Sprintf(errEventStr, "500", "handleErrorResponse:json unmarshal err", err.Error()+"data:"+errStr))
	}
}

const errEventStr = `{"event":"error","status":"%v","code":"%s","message":"%s"}`

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}
