package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// 环境变量键
const (
	HA_URL_KEY   = "HA_URL"
	HA_TOKEN_KEY = "HA_TOKEN"
)

// 默认 Home Assistant URL
const DEFAULT_HA_URL = "https://10.0.0.129:8123"

// 全局错误变量
var (
	ErrInvalidParams  = errors.New("invalid parameters")
	ErrMethodNotFound = errors.New("method not found")
)

// MCP 请求结构
type MCPRequest struct {
	Type   string          `json:"type"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// MCP 响应结构
type MCPResponse struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// 工具描述结构
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// MCP 错误结构
type MCPError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type HomeAssistantServer struct {
	HAURL    string
	HAToken  string
	Client   *http.Client
	Tools    []Tool
	ExitChan chan os.Signal
}

func NewHomeAssistantServer(haURL, haToken string) *HomeAssistantServer {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	return &HomeAssistantServer{
		HAURL:    haURL,
		HAToken:  haToken,
		Client:   client,
		ExitChan: make(chan os.Signal, 1),
	}
}

func (s *HomeAssistantServer) InitializeTools() {
	s.Tools = []Tool{
		{
			Name:        "get_state",
			Description: "获取 Home Assistant 实体的当前状态",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"entity_id": map[string]interface{}{
						"type":        "string",
						"description": "要获取状态的实体 ID（例如：light.living_room）",
					},
				},
				"required": []string{"entity_id"},
			},
		},
		{
			Name:        "toggle_entity",
			Description: "切换 Home Assistant 实体的开/关状态",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"entity_id": map[string]interface{}{
						"type":        "string",
						"description": "要切换的实体 ID（例如：switch.bedroom）",
					},
					"state": map[string]interface{}{
						"type":        "string",
						"description": "期望的状态（on/off）",
						"enum":        []string{"on", "off"},
					},
				},
				"required": []string{"entity_id", "state"},
			},
		},
		{
			Name:        "trigger_automation",
			Description: "触发 Home Assistant 自动化",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"automation_id": map[string]interface{}{
						"type":        "string",
						"description": "要触发的自动化 ID（例如：automation.morning_routine）",
					},
				},
				"required": []string{"automation_id"},
			},
		},
		{
			Name:        "list_entities",
			Description: "列出 Home Assistant 中所有可用的实体",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]interface{}{
						"type":        "string",
						"description": "可选的领域过滤器（例如：light, switch, automation）",
					},
				},
			},
		},
	}
}

func (s *HomeAssistantServer) getEntityState(args map[string]interface{}) (interface{}, *MCPError) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return nil, &MCPError{Code: "InvalidParams", Message: "entity_id 是必需的"}
	}

	url := fmt.Sprintf("%s/api/states/%s", strings.TrimRight(s.HAURL, "/"), entityID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &MCPError{Code: "HomeAssistantAPIError", Message: fmt.Sprintf("状态码: %d", resp.StatusCode)}
	}

	var data interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}

	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": toPrettyJSON(data),
			},
		},
	}, nil
}

func (s *HomeAssistantServer) toggleEntity(args map[string]interface{}) (interface{}, *MCPError) {
	entityID, ok := args["entity_id"].(string)
	if !ok || entityID == "" {
		return nil, &MCPError{Code: "InvalidParams", Message: "entity_id 是必需的"}
	}

	state, ok := args["state"].(string)
	if !ok || (state != "on" && state != "off") {
		return nil, &MCPError{Code: "InvalidParams", Message: "state 必须为 'on' 或 'off'"}
	}

	service := "turn_" + state
	url := fmt.Sprintf("%s/api/services/homeassistant/%s", strings.TrimRight(s.HAURL, "/"), service)

	payload := map[string]interface{}{
		"entity_id": entityID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(body)))
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &MCPError{Code: "HomeAssistantAPIError", Message: fmt.Sprintf("状态码: %d", resp.StatusCode)}
	}

	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("成功将 %s 设置为 %s", entityID, state),
			},
		},
	}, nil
}

func (s *HomeAssistantServer) triggerAutomation(args map[string]interface{}) (interface{}, *MCPError) {
	automationID, ok := args["automation_id"].(string)
	if !ok || automationID == "" {
		return nil, &MCPError{Code: "InvalidParams", Message: "automation_id 是必需的"}
	}

	url := fmt.Sprintf("%s/api/services/automation/trigger", strings.TrimRight(s.HAURL, "/"))

	payload := map[string]interface{}{
		"entity_id": automationID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(body)))
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &MCPError{Code: "HomeAssistantAPIError", Message: fmt.Sprintf("状态码: %d", resp.StatusCode)}
	}

	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": fmt.Sprintf("成功触发自动化 %s", automationID),
			},
		},
	}, nil
}

func (s *HomeAssistantServer) listEntities(args map[string]interface{}) (interface{}, *MCPError) {
	url := fmt.Sprintf("%s/api/states", strings.TrimRight(s.HAURL, "/"))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &MCPError{Code: "HomeAssistantAPIError", Message: fmt.Sprintf("状态码: %d", resp.StatusCode)}
	}

	var entities []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&entities); err != nil {
		return nil, &MCPError{Code: "InternalError", Message: err.Error()}
	}

	// 如果提供了 domain 过滤
	if domain, ok := args["domain"].(string); ok && domain != "" {
		filtered := []map[string]interface{}{}
		prefix := domain + "."
		for _, entity := range entities {
			if strings.HasPrefix(entity["entity_id"].(string), prefix) {
				filtered = append(filtered, entity)
			}
		}
		entities = filtered
	}

	// 简化实体信息
	simplified := []map[string]interface{}{}
	for _, entity := range entities {
		simplified = append(simplified, map[string]interface{}{
			"entity_id":  entity["entity_id"],
			"state":      entity["state"],
			"attributes": entity["attributes"],
		})
	}

	return map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": toPrettyJSON(simplified),
			},
		},
	}, nil
}

// 辅助函数：将任意接口格式化为 JSON 字符串
func toPrettyJSON(v interface{}) string {
	bytes, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("JSON 序列化错误: %v", err)
	}
	return string(bytes)
}

func (s *HomeAssistantServer) handleRequest(req MCPRequest) MCPResponse {
	switch req.Method {
	case "list_tools":
		return s.handleListTools()
	case "call_tool":
		return s.handleCallTool(req.Params)
	default:
		return MCPResponse{
			Type: "error",
			Error: &MCPError{
				Code:    "MethodNotFound",
				Message: fmt.Sprintf("未知方法: %s", req.Method),
			},
		}
	}
}

func (s *HomeAssistantServer) handleListTools() MCPResponse {
	return MCPResponse{
		Type: "response",
		Content: map[string]interface{}{
			"tools": s.Tools,
		},
	}
}

func (s *HomeAssistantServer) handleCallTool(params json.RawMessage) MCPResponse {
	// 定义参数结构
	var callParams struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &callParams); err != nil {
		return MCPResponse{
			Type: "error",
			Error: &MCPError{
				Code:    "InvalidParams",
				Message: "参数解析错误",
			},
		}
	}

	// 根据工具名称调用相应方法
	var result interface{}
	var mcpErr *MCPError

	switch callParams.Name {
	case "get_state":
		result, mcpErr = s.getEntityState(callParams.Arguments)
	case "toggle_entity":
		result, mcpErr = s.toggleEntity(callParams.Arguments)
	case "trigger_automation":
		result, mcpErr = s.triggerAutomation(callParams.Arguments)
	case "list_entities":
		result, mcpErr = s.listEntities(callParams.Arguments)
	default:
		mcpErr = &MCPError{
			Code:    "MethodNotFound",
			Message: fmt.Sprintf("未知工具: %s", callParams.Name),
		}
	}

	if mcpErr != nil {
		return MCPResponse{
			Type:  "error",
			Error: mcpErr,
		}
	}

	return MCPResponse{
		Type:    "response",
		Content: result,
	}
}

func (s *HomeAssistantServer) sendResponse(resp MCPResponse) {
	bytes, err := json.Marshal(resp)
	if err != nil {
		log.Printf("响应序列化错误: %v\n", err)
		return
	}
	fmt.Println(string(bytes))
}

func (s *HomeAssistantServer) Run() {
	// 初始化工具
	s.InitializeTools()

	// 设置信号监听
	signal.Notify(s.ExitChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	// 启动 goroutine 监听信号
	go func() {
		<-s.ExitChan
		log.Println("接收到退出信号，正在关闭服务器...")
		os.Exit(0)
	}()

	// 读取标准输入
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		var req MCPRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			resp := MCPResponse{
				Type: "error",
				Error: &MCPError{
					Code:    "InvalidRequest",
					Message: "请求解析错误",
				},
			}
			s.sendResponse(resp)
			continue
		}

		resp := s.handleRequest(req)
		s.sendResponse(resp)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("读取标准输入错误: %v\n", err)
	}
}

func main() {
	// 读取环境变量
	haURL := os.Getenv(HA_URL_KEY)
	if haURL == "" {
		haURL = DEFAULT_HA_URL
	}
	haToken := os.Getenv(HA_TOKEN_KEY)
	if haToken == "" {
		log.Fatal("环境变量 HA_TOKEN 是必需的")
	}

	// 创建服务器实例
	server := NewHomeAssistantServer(haURL, haToken)

	// 运行服务器
	log.Println("Home Assistant MCP 服务器正在通过 stdio 运行")
	server.Run()
}
