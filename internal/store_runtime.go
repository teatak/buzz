package buzz

func (s *Store) ReloadRuntime() (map[string]AuthToken, []ProviderRecord, []APIKey, error) {
	tokens, err := s.AuthTokens()
	if err != nil {
		return nil, nil, nil, err
	}
	providers, err := s.EnabledProviders()
	if err != nil {
		return nil, nil, nil, err
	}
	keys, err := s.RuntimeProviderAPIKeys()
	if err != nil {
		return nil, nil, nil, err
	}
	return tokens, providers, keys, nil
}
