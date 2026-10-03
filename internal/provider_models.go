package buzz

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/teatak/cart/v3"
)

func (s *Server) handleProviderUpstreamModelsAdmin(c *cart.Context) error {
	providerID, err := c.ParamInt64("id")
	if err != nil {
		c.JSON(http.StatusBadRequest, cart.H{"error": "invalid provider id"})
		return nil
	}

	provider, err := s.store.Provider(providerID)
	if err != nil {
		c.JSON(http.StatusNotFound, cart.H{"error": "provider not found"})
		return nil
	}
	if len(provider.Endpoints) == 0 {
		c.JSON(http.StatusBadRequest, cart.H{"error": "provider has no endpoints"})
		return nil
	}
	protocol := normalizeRouteProtocol(c.Request.URL.Query().Get("protocol"))
	var endpoint ProviderEndpoint
	if protocol == providerAuto {
		for _, preferred := range []string{providerOpenAI, providerOpenAIResponses, providerAnthropic, providerGemini} {
			for _, candidate := range provider.Endpoints {
				if candidate.Enabled && strings.EqualFold(candidate.Protocol, preferred) {
					endpoint = candidate
					break
				}
			}
			if endpoint.ID != 0 {
				break
			}
		}
	} else {
		for _, candidate := range provider.Endpoints {
			if candidate.Enabled && strings.EqualFold(candidate.Protocol, protocol) {
				endpoint = candidate
				break
			}
		}
	}
	if endpoint.ID == 0 {
		c.JSON(http.StatusBadRequest, cart.H{"error": "provider does not have an enabled endpoint for the selected protocol"})
		return nil
	}

	keys, err := s.store.ProviderKeys(providerID, true)
	if err != nil || len(keys) == 0 {
		c.JSON(http.StatusBadRequest, cart.H{"error": "provider has no active keys"})
		return nil
	}

	var activeKey ProviderKey
	found := false
	for _, k := range keys {
		if k.Enabled && k.DisabledStatus == 0 {
			activeKey = k
			found = true
			break
		}
	}
	if !found {
		activeKey = keys[0] // fallback
	}

	baseURL := endpoint.BaseURL
	path := "/v1/models"
	if endpoint.Protocol == providerGemini {
		path = "/v1beta/models"
	}
	url := providerRequestPath(baseURL, path)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, cart.H{"error": "failed to create request"})
		return nil
	}

	if endpoint.Protocol == providerAnthropic {
		req.Header.Set("x-api-key", activeKey.Secret)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if endpoint.Protocol == providerGemini {
		query := req.URL.Query()
		query.Set("key", activeKey.Secret)
		req.URL.RawQuery = query.Encode()
	} else {
		req.Header.Set("Authorization", "Bearer "+activeKey.Secret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, cart.H{"error": "failed to fetch upstream models", "details": err.Error()})
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		c.JSON(http.StatusBadGateway, cart.H{"error": "upstream returned error", "status": resp.StatusCode})
		return nil
	}

	models, err := decodeUpstreamModels(resp.Body, endpoint.Protocol)
	if err != nil {
		c.JSON(http.StatusBadGateway, cart.H{"error": "failed to decode upstream response"})
		return nil
	}
	c.JSON(http.StatusOK, models)
	return nil
}
