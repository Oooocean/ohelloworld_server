package main

import (
    "encoding/json"
    "fmt"
    "time"
    "net/http"
    "math/rand"
    "github.com/gorilla/websocket"
    "github.com/satori/go.uuid"
)

type ClientMessage struct {
    client *Client
    msg *MessageObj
}
type ClientManager struct {
    ticker     *time.Ticker
    updateTicker  *time.Ticker
    clients    map[*Client]bool
    rooms      []*Room
    broadcast  chan []byte
    register   chan *Client
    unregister chan *Client
    startmatch  chan *Client
    cancelmatch chan *Client
    inputAction chan *ClientMessage
}

type Client struct {
    id     string
    name   string
    avatar string
    socket *websocket.Conn
    send   chan []byte
    status  int               // 当前状态0空闲，1匹配中
    status_time time.Time
    room   *Room
}

type Action struct {
    OpLeft bool       `json:"l,omitempty"`
    OpRight bool      `json:"r,omitempty"`
}

type Frame struct {
    Actions []Action    `json:"actions,omitempty"`
    FrameId int         `json:"frame_id,omitempty"`
}

type Room struct {
    clients []*Client
    frameSpan time.Duration
    startTime time.Time
    lastUpdateTime  int64
    accTime         int64
    frames          []*Frame
    curFrameId      int
    frameInterval   int64
    status          int    
}

type Message struct {
    Sender    string `json:"sender,omitempty"`
    Recipient string `json:"recipient,omitempty"`
    Content   string `json:"content,omitempty"`
}

type MessageObj struct {  
    Cmd     int              `json:"cmd,omitempty"`  
    Body    string           `json:"body,omitempty"`  
}
type MessageCommonRsp struct {  
    Ret     int      `json:"ret,omitempty"`   
}  
type MessageLoginReq struct {  
    Name         string      `json:"name,omitempty"`  
    AvatarURL    string      `json:"avatarUrl,omitempty"`  
}    
type MessageMatchNotify struct {  
    Ret         int      `json:"ret,omitempty"`
    Players []ClientPlayer  `json:"players,omitempty"`  
}
type MessageGameReadyReq struct {
}  
type MessageInputReq struct {
    OpLeft bool       `json:"l,omitempty"`
    OpRight bool      `json:"r,omitempty"`
} 
type MessageFrameNotify struct {  
    Status         int      `json:"status,omitempty"`  
} 
type ClientPlayer struct {
    Name string `json:"name,omitempty"`
    AvatarURL string `json:"avatar,omitempty"`
}
// 102
type MessageGameStartNotify struct {  
    Seed int                `json:"seed,omitempty"`
    Players []ClientPlayer  `json:"players,omitempty"`
    // 游戏总时间
    TotalGameTimeMs int     `json:"total_game_time_ms,omitempty"`
    // 胜利条件
    TotalWinCount  int      `json:"total_win_count,omitempty"`
}
// 103
type MessageGameEndReq struct {  
} 
// 104
type MessageGameEndNotify struct {  
}

const (
    CLIENT_LOGINED  = iota // value --> 0
    CLIENT_MATCHING        // value --> 1
    CLIENT_WAIT_READY      // value --> 2
    CLIENT_READY           // value --> 3
    CLIENT_PLAYING         // value --> 4
    CLIENT_DISCONNECTED    // value --> 5
)

const (
    ROOM_WAIT_READY  = iota     // value --> 0
    ROOM_PLAY                   // value --> 1
    ROOM_WAIT_DESTROY           // value --> 2 
)


func (room *Room) update(msNow int64) {
    if (room.status == ROOM_WAIT_DESTROY) {
        // 游戏已经结束
        // TODO: 等待删除释放room
        return
    }
    if (room.status == ROOM_WAIT_READY)    {
        var allReady = true
        for _, conn := range room.clients {
            if (conn.status != CLIENT_READY) {
                allReady = false
                break
            }
            fmt.Println(conn.status)
        }
        if (allReady) {
            for _, conn := range room.clients {
                conn.status = CLIENT_PLAYING
            }
            fmt.Println("all ready to play")
            room.status = ROOM_PLAY
            var msg = &MessageGameStartNotify{
                Seed: 12345,
                TotalGameTimeMs: 30000,
                TotalWinCount: 10,
            }
            room.broadcastData(102, msg)
        }
        return
    }
    var dt = msNow - room.lastUpdateTime 
    room.accTime += dt
    for room.accTime >= room.frameInterval {
        room.broadcastActions()
        room.curFrameId++
        var frame = &Frame{
            FrameId : room.curFrameId,
            Actions : make([]Action, 2),
        }
        room.frames = append(room.frames, frame)
        room.accTime -= room.frameInterval
    }
    room.lastUpdateTime = msNow
}

func (room *Room) broadcastActions() {
    var frame = room.frames[room.curFrameId]
    for _, conn := range room.clients {
        if (conn.status == CLIENT_DISCONNECTED) {
            continue
        }
        conn.sendClientMsg(10, frame)
    }
}

func (room *Room) broadcastData(cmd int, bodyObj interface{}) {
    for _, conn := range room.clients {
        if (conn.status == CLIENT_DISCONNECTED) {
            continue
        }
        conn.sendClientMsg(cmd, bodyObj)
    }
}

func (room *Room) addPlayer(conn *Client) {
    conn.status = CLIENT_WAIT_READY
    conn.status_time = time.Now()
    conn.room = room
    room.clients = append(room.clients, conn)
}

func (room *Room) onMatchComplete() {
                    // 初始化第一帧
                    var frame = &Frame{
                        FrameId: 0,
                        Actions : make([]Action, 2),
                    }
                    room.frames = append(room.frames, frame) 
    
                    // 发送匹配结果
                    var matchNotify = &MessageMatchNotify{Ret:0}
                    for _, conn := range room.clients {
                        var clientPlayer ClientPlayer
                        clientPlayer.Name = conn.name
                        clientPlayer.AvatarURL = conn.avatar
                        matchNotify.Players = append(matchNotify.Players, clientPlayer)
                    fmt.Println("match complete.", conn.name)
                    }
                    room.broadcastData(8, matchNotify)
}

var manager = ClientManager{
    ticker:     time.NewTicker(100 * time.Millisecond),
    updateTicker:     time.NewTicker(10 * time.Millisecond),
    broadcast:  make(chan []byte),
    register:   make(chan *Client),
    unregister: make(chan *Client),
    clients:    make(map[*Client]bool),
    startmatch: make(chan *Client),
    cancelmatch: make(chan *Client),
    inputAction: make(chan *ClientMessage),
}

func (manager *ClientManager) checkMatchComplete() {
    var now = time.Now().Unix()
    var waitMatchPlayerList []*Client; 
    for conn := range manager.clients {
        if (conn.status == CLIENT_MATCHING && (now - conn.status_time.Unix() > 1) ) {
            waitMatchPlayerList = append(waitMatchPlayerList, conn);
        }
    }
    for i:= 0; i < len(waitMatchPlayerList) / 2; i++ {
        // 匹配超时(暂时处理为单人房间)
        var conn1 = waitMatchPlayerList[i * 2]
        var conn2 = waitMatchPlayerList[i * 2 + 1]
        var room = &Room{
            lastUpdateTime:  time.Now().UnixNano() / 1000000,
            curFrameId : 0,
            accTime: 0,
            frameInterval: 30,
            status: ROOM_WAIT_READY,
        }
        room.addPlayer(conn1)
        room.addPlayer(conn2)
        manager.rooms = append(manager.rooms, room)
        room.onMatchComplete()
    }
    if (len(waitMatchPlayerList) % 2 != 0) {
        // 超过10s， 单人开始
        var conn = waitMatchPlayerList[len(waitMatchPlayerList) -1]
        if now - conn.status_time.Unix() < 1 {
            return
        } 

        var room = &Room{
            lastUpdateTime:  time.Now().UnixNano() / 1000000,
            curFrameId : 0,
            accTime: 0,
            frameInterval: 30,
            status: ROOM_WAIT_READY,
        }
        room.addPlayer(conn)
        manager.rooms = append(manager.rooms, room)
        room.onMatchComplete()
    }
}

func (manager *ClientManager) start() {
    for {
        select {
        case <-manager.ticker.C:
            manager.checkMatchComplete();
        case <-manager.updateTicker.C:
            var now = time.Now().UnixNano() / 1000000
            for _, room := range manager.rooms {
                room.update(now)
            }
        case conn := <-manager.register:
            manager.clients[conn] = true
            jsonMessage, _ := json.Marshal(&Message{Content: "/A new socket has connected."})
            fmt.Println(string(jsonMessage))
            // manager.send(jsonMessage, conn)
        case conn := <-manager.unregister:
            if _, ok := manager.clients[conn]; ok {
                close(conn.send)
                delete(manager.clients, conn)
                conn.status = CLIENT_DISCONNECTED
                jsonMessage, _ := json.Marshal(&Message{Content: "/A socket has disconnected."})
                fmt.Println(string(jsonMessage))
                // manager.send(jsonMessage, conn)
            }
        case conn := <-manager.startmatch:
            if _, ok := manager.clients[conn]; ok {
                if (conn.status == CLIENT_LOGINED) {
                    conn.status = CLIENT_MATCHING
                    conn.status_time = time.Now()
                    fmt.Println("player start match", conn.name)
                }
            }
        case conn := <-manager.cancelmatch:
            if _, ok := manager.clients[conn]; ok {
                if (conn.status == 1) {
                    conn.status = CLIENT_LOGINED
                    conn.status_time = time.Now()
                    fmt.Println("player cancel match", conn.name)
                }
            }
        case cmsg := <-manager.inputAction:
            var actionClient = cmsg.client 
            var room = actionClient.room
            if (cmsg.msg.Cmd == 11) {
                    var inputReq MessageInputReq
                    json.Unmarshal([]byte(cmsg.msg.Body), &inputReq)
                    for playerIndex, client := range room.clients {
                        if (client == actionClient) {
                            var action = &room.frames[room.curFrameId].Actions[playerIndex];
                            action.OpLeft = inputReq.OpLeft
                            action.OpRight = inputReq.OpRight
                            if (action.OpLeft || action.OpRight) {
                                fmt.Println("recv actions!",  playerIndex, inputReq.OpLeft,  inputReq.OpRight)
                            }
                        }
                    }
                } else if (cmsg.msg.Cmd == 101) {
                    // 准备
                    actionClient.status = CLIENT_READY
                    fmt.Println("CLIENT_READY")
                } else if (cmsg.msg.Cmd == 103) {
                    // 游戏结束
                    room.status = ROOM_WAIT_DESTROY
                    actionClient.status = CLIENT_LOGINED
                    var endNotify = &MessageGameEndNotify{}
                    room.broadcastData(104, endNotify)
                }
        case message := <-manager.broadcast:
            for conn := range manager.clients {
                select {
                case conn.send <- message:
                default:
                    close(conn.send)
                    delete(manager.clients, conn)
                }
            }
        }
    }
}

func (manager *ClientManager) send(message []byte, ignore *Client) {
    for conn := range manager.clients {
        if conn != ignore {
            conn.send <- message
        }
    }
}

func (client *Client) sendClientMsg(cmd int, bodyObj interface{}) {
    body, _ := json.Marshal(bodyObj)
    data, _ := json.Marshal(&MessageObj{Cmd: cmd,  Body: string(body)})
    if cmd != 10 {
        fmt.Println("sendToClient called.", string(data))
    }
    client.send <- data
}

func (client *Client) read() {
    defer func() {
        manager.unregister <- client
        client.socket.Close()
    }()

    for {
        _, message, err := client.socket.ReadMessage()
        if err != nil {
            manager.unregister <- client
            client.socket.Close()
            break
        }
        var msg = &MessageObj{}
        err = json.Unmarshal(message, &msg)
        // fmt.Println(msg, err)

        switch msg.Cmd { 
        case 1:
            // 登录请求
            var loginReq MessageLoginReq
            err = json.Unmarshal([]byte(msg.Body), &loginReq)
            fmt.Println("loginReq:", loginReq)
            client.name   = loginReq.Name
            client.avatar = loginReq.AvatarURL
            client.sendClientMsg(msg.Cmd + 1, &MessageCommonRsp{Ret:0})
        case 3:
            // 开始匹配
            manager.startmatch <- client
        case 5:
            // 取消匹配 
            manager.cancelmatch <- client
        case 11:
            fallthrough
        case 101:
            fallthrough
        case 103:
            var cmsg = &ClientMessage{
                client: client,
                msg: msg,
            }
            manager.inputAction <- cmsg
        }


        // jsonMessage, _ := json.Marshal(&Message{Sender: c.id, Content: string(message)})
        // manager.broadcast <- jsonMessage
    }
}

func (c *Client) write() {
    defer func() {
        c.socket.Close()
    }()

    for {
        select {
        case message, ok := <-c.send:
            if !ok {
                c.socket.WriteMessage(websocket.CloseMessage, []byte{})
                return
            }

            c.socket.WriteMessage(websocket.TextMessage, message)
        }
    }
}

func main() {
    rand.Seed(time.Now().UnixNano())
    fmt.Println("Starting application...")
    go manager.start()
    http.HandleFunc("/", wsPage)
    http.ListenAndServeTLS(":443", "cert.pem", "key.pem", nil)
}

func wsPage(res http.ResponseWriter, req *http.Request) {
    conn, error := (&websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}).Upgrade(res, req, nil)
    if error != nil {
        http.NotFound(res, req)
        return
    }
    u, _ := uuid.NewV4()
    client := &Client{id: u.String(), socket: conn, send: make(chan []byte)}

    manager.register <- client

    go client.read()
    go client.write()
}

