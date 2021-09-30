package apiclient

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/anywherelan/awl/api"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
)

type Client struct {
	address string
	cli     *http.Client
}

func New(address string) *Client {
	return &Client{
		address: address,
		cli: &http.Client{
			Transport: &http.Transport{},
			Timeout:   time.Second,
		},
	}
}

func (c *Client) KnownPeers() ([]entity.KnownPeersResponse, error) {
	knownPeers := make([]entity.KnownPeersResponse, 0)
	err := c.sendGetRequest(api.GetKnownPeersPath, &knownPeers)
	if err != nil {
		return nil, err
	}
	return knownPeers, nil
}

func (c *Client) KnownPeerConfig(peerID string) (*config.KnownPeer, error) {
	knownPeer := new(config.KnownPeer)
	request := entity.PeerIDRequest{PeerID: peerID}
	err := c.sendPostRequest(api.GetKnownPeerSettingsPath, request, knownPeer)
	if err != nil {
		return nil, err
	}
	return knownPeer, nil
}

func (c *Client) PeerInfo() (*entity.PeerInfo, error) {
	peerInfo := new(entity.PeerInfo)
	err := c.sendGetRequest(api.GetMyPeerInfoPath, peerInfo)
	if err != nil {
		return nil, err
	}
	return peerInfo, nil
}

func (c *Client) SendFriendRequest(peerID, alias string) error {
	request := entity.FriendRequest{
		PeerID: peerID,
		Alias:  alias,
	}
	return c.sendPostRequest(api.SendFriendRequestPath, request, nil)
}

func (c *Client) AcceptFriendRequest(peerID, alias string) error {
	request := entity.FriendRequest{
		PeerID: peerID,
		Alias:  alias,
	}
	return c.sendPostRequest(api.AcceptPeerInvitationPath, request, nil)
}

func (c *Client) AuthRequests() ([]entity.AuthRequest, error) {
	authRequests := make([]entity.AuthRequest, 0)
	err := c.sendGetRequest(api.GetAuthRequestsPath, &authRequests)
	if err != nil {
		return nil, err
	}
	return authRequests, nil
}

func (c *Client) UpdatePeerSettings(request entity.UpdatePeerSettingsRequest) error {
	return c.sendPostRequest(api.UpdatePeerSettingsPath, request, nil)
}

func (c *Client) RemovePeer(peerID string) error {
	request := entity.PeerIDRequest{PeerID: peerID}
	return c.sendPostRequest(api.RemovePeerSettingsPath, request, nil)
}

func (c *Client) UpdateMySettings(name string) error {
	request := entity.UpdateMySettingsRequest{
		Name: name,
	}
	return c.sendPostRequest(api.UpdateMyInfoPath, request, nil)
}

func (c *Client) P2pDebugInfo() (*entity.P2pDebugInfo, error) {
	debugInfo := new(entity.P2pDebugInfo)
	err := c.sendGetRequest(api.GetP2pDebugInfoPath, debugInfo)
	if err != nil {
		return nil, err
	}
	return debugInfo, nil
}

func (c *Client) ApplicationLog() (string, error) {
	resp, err := c.cli.Get(c.getUrl(api.GetDebugLogPath))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	return string(b), err
}

func (c *Client) getUrl(methodPath string) string {
	return "http://" + c.address + methodPath
}

func (c *Client) sendGetRequest(path string, responseRef interface{}) error {
	resp, err := c.cli.Get(c.getUrl(path))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return c.readResponseBody(resp, responseRef)
}

func (c *Client) sendPostRequest(path string, payload interface{}, responseRef interface{}) error {
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(payload)
	if err != nil {
		return err
	}

	resp, err := c.cli.Post(c.getUrl(path), "application/json", buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return c.readResponseBody(resp, responseRef)
}

func (c *Client) readResponseBody(resp *http.Response, responseRef interface{}) error {
	if resp.StatusCode != http.StatusOK {
		apiError := api.Error{}
		err := json.NewDecoder(resp.Body).Decode(&apiError)
		if err != nil {
			return err
		}
		return apiError
	} else if responseRef != nil {
		err := json.NewDecoder(resp.Body).Decode(responseRef)
		if err != nil {
			return err
		}
	} else {
		_, _ = io.Copy(ioutil.Discard, resp.Body)
	}

	return nil
}
