package main

import "fmt"

// RoomEndpoint 代表一次参与身份的本地端点。重新加入必须创建新 pid 与新端
// 点；旧端点上收到的任何答复仍带旧 pid，会被状态机拒绝。
type RoomEndpoint struct {
	roomID  string
	pid     string
	service *RoomService
	bus     *RoomBus
}

func NewRoomEndpoint(roomID, pid string, service *RoomService, bus *RoomBus, autoNotify bool) *RoomEndpoint {
	endpoint := &RoomEndpoint{roomID: roomID, pid: pid, service: service, bus: bus}
	bus.Register(pid, func(message RoomMessage) {
		if autoNotify {
			fmt.Printf("%s 收到 %s 的协商请求 adj#%d 项%d：轨道 %s#%d\n",
				pid, message.FromPID, message.AdjustmentID, message.ItemIndex, message.TrackID, message.TrackGen)
		}
	})
	return endpoint
}

func (e *RoomEndpoint) PID() string { return e.pid }

func (e *RoomEndpoint) Connect() []RoomMessage { return e.bus.Connect(e.pid) }

func (e *RoomEndpoint) Disconnect() { e.bus.Disconnect(e.pid) }

// Resync 让断线者重新取得当前可参与轨道与协商概况；它只反映状态，不替新
// 参与身份恢复任何已经撤回的订阅。
func (e *RoomEndpoint) Resync() (*Overview, error) {
	return e.service.OverviewFor(e.roomID, e.pid)
}

func (e *RoomEndpoint) Publish(id, kind string, params MediaParams) (*RTrack, error) {
	return e.service.PublishTrack(e.roomID, e.pid, id, kind, params)
}

func (e *RoomEndpoint) Remove(id string) (*Adjustment, error) {
	return e.service.RemoveTrack(e.roomID, e.pid, id)
}

func (e *RoomEndpoint) Unsubscribe(id string, generation int) error {
	return e.service.Unsubscribe(e.roomID, e.pid, id, generation)
}

func (e *RoomEndpoint) Offer(id string, params MediaParams, targets []string) (*Adjustment, error) {
	adjust, err := e.service.Offer(e.roomID, e.pid, id, params, targets)
	if err != nil {
		return nil, err
	}
	for i, item := range adjust.Items {
		message := RoomMessage{
			Type:         RoomMessageOffer,
			RoomID:       e.roomID,
			FromPID:      e.pid,
			ToPID:        item.TargetPID,
			AdjustmentID: adjust.ID,
			ItemIndex:    i,
			TrackID:      item.TrackID,
			TrackGen:     item.TrackGen,
			Params:       item.Offer,
		}
		if err := e.bus.Send(message); err != nil {
			return nil, err
		}
	}
	return adjust, nil
}

func (e *RoomEndpoint) Subscribe(id string, params MediaParams) (*Adjustment, error) {
	adjust, err := e.service.RequestSubscribe(e.roomID, e.pid, id, params)
	if err != nil {
		return nil, err
	}
	for i, item := range adjust.Items {
		message := RoomMessage{
			Type:         RoomMessageSubscribe,
			RoomID:       e.roomID,
			FromPID:      e.pid,
			ToPID:        item.TargetPID,
			AdjustmentID: adjust.ID,
			ItemIndex:    i,
			TrackID:      item.TrackID,
			TrackGen:     item.TrackGen,
			Params:       item.Offer,
		}
		if err := e.bus.Send(message); err != nil {
			return nil, err
		}
	}
	return adjust, nil
}

func (e *RoomEndpoint) Respond(adjustmentID, itemIndex int, accept bool, params MediaParams, reason string) (*Adjustment, bool, error) {
	return e.service.Respond(e.roomID, e.pid, adjustmentID, itemIndex, accept, params, reason)
}
