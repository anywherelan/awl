package client

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/peerlan/peerlan/api"
	"github.com/peerlan/peerlan/config"
	"github.com/peerlan/peerlan/entity"
)

// TODO: удалить пакет? он был нужен только для awl-fyne

type Client struct {
	address string
}

func New(address string) *Client {
	return &Client{
		address: address,
	}
}

func (c *Client) KnownPeers() []entity.KnownPeersResponse {
	knownPeers := make([]entity.KnownPeersResponse, 0)
	c.sendGetRequest(api.GetKnownPeersPath, &knownPeers)
	return knownPeers
}

func (c *Client) KnownPeerConfig(peerID string) (*config.KnownPeer, error) {
	knownPeer := new(config.KnownPeer)
	payload := entity.PeerIDRequest{PeerID: peerID}

	err := c.sendPostRequest(api.GetKnownPeerSettingsPath, &payload, knownPeer)
	if err != nil {
		return nil, err
	}
	return knownPeer, nil
}

func (c *Client) PeerInfo() entity.PeerInfo {
	var peerInfo entity.PeerInfo
	c.sendGetRequest(api.GetMyPeerInfoPath, &peerInfo)
	return peerInfo
}

func (c *Client) P2pDebugInfo() entity.P2pDebugInfo {
	var debugInfo entity.P2pDebugInfo
	c.sendGetRequest(api.GetP2pDebugInfoPath, &debugInfo)
	return debugInfo
}

func (c *Client) ForwardedPorts() []entity.ForwardedPort {
	connections := make([]entity.ForwardedPort, 0)
	c.sendGetRequest(api.GetForwardedPortsPath, &connections)
	return connections
}

func (c *Client) InboundConnections() map[int][]entity.InboundStream {
	connections := make(map[int][]entity.InboundStream)
	c.sendGetRequest(api.GetInboundConnectionsPath, &connections)
	return connections
}

func (c *Client) SendFriendRequest(peerID, alias string) error {
	payload := entity.FriendRequest{
		PeerID: peerID,
		Alias:  alias,
	}
	return c.sendPostRequest(api.SendFriendRequestPath, &payload, nil)
}

func (c *Client) AcceptFriendRequest(peerID, alias string) error {
	payload := entity.FriendRequest{
		PeerID: peerID,
		Alias:  alias,
	}
	return c.sendPostRequest(api.AcceptPeerInvitationPath, &payload, nil)
}

func (c *Client) AuthRequests() []entity.AuthRequest {
	authRequests := make([]entity.AuthRequest, 0)
	c.sendGetRequest(api.GetAuthRequestsPath, &authRequests)
	return authRequests
}

func (c *Client) UpdatePeerSettings(req entity.UpdatePeerSettingsRequest) error {
	return c.sendPostRequest(api.UpdatePeerSettingsPath, &req, nil)
}

// TODO: update
func (c *Client) UpdateMySettings(name string) error {
	payload := entity.UpdateMySettingsRequest{
		Name: name,
	}
	return c.sendPostRequest(api.UpdateMyInfoPath, &payload, nil)
}

func (c *Client) DebugInfo() json.RawMessage {
	payload := make(json.RawMessage, 0)
	//payload := new(entity.P2pDebugInfo)
	c.sendGetRequest(api.GetP2pDebugInfoPath, &payload)
	// REMOVE
	//var out bytes.Buffer
	//_ = json.Indent(&out, payload, "", "    ")
	//return out.Bytes()
	return payload
}

func (c *Client) DebugLog() string {
	url := "http://" + c.address + api.GetDebugLogPath
	resp, err := http.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	b, _ := ioutil.ReadAll(resp.Body)
	return string(b)
}

// TODO: добавить timeout
func (c *Client) sendGetRequest(path string, outRef interface{}) {
	url := "http://" + c.address + path
	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(outRef)
	if err != nil {
		return
	}
}

// TODO: добавить timeout
func (c *Client) sendPostRequest(path string, payload interface{}, responseRef interface{}) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(payload)
	if err != nil {
		return err
	}

	url := "http://" + c.address + path
	resp, err := http.Post(url, "application/json", buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		apiError := api.Error{}
		err = json.NewDecoder(resp.Body).Decode(&apiError)
		if err != nil {
			return err
		}
		return apiError
	} else if responseRef != nil {
		err = json.NewDecoder(resp.Body).Decode(responseRef)
		if err != nil {
			return err
		}
	}

	return nil
}
