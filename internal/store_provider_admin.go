package buzz

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

func (s *Store) Providers() ([]ProviderRecord, error) {
	rows, err := s.query(`
		SELECT id, name, preset_id, enabled, created_at, updated_at
		FROM providers
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProviderRecord, 0)
	for rows.Next() {
		var item ProviderRecord
		var enabled int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.PresetID, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		item.CreatedAt = formatStoreTime(createdAt)
		item.UpdatedAt = formatStoreTime(updatedAt)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.hydrateProviderEndpoints(out)
}

func (s *Store) Provider(id int64) (ProviderRecord, error) {
	var item ProviderRecord
	var enabled int
	var createdAt, updatedAt time.Time
	err := s.queryRow(`
		SELECT id, name, preset_id, enabled, created_at, updated_at
		FROM providers
		WHERE id = ?`,
		id,
	).Scan(&item.ID, &item.Name, &item.PresetID, &enabled, &createdAt, &updatedAt)
	if err != nil {
		return item, err
	}
	item.Enabled = enabled != 0
	item.CreatedAt = formatStoreTime(createdAt)
	item.UpdatedAt = formatStoreTime(updatedAt)
	items, err := s.hydrateProviderEndpoints([]ProviderRecord{item})
	if err != nil {
		return item, err
	}
	return items[0], nil
}

func (s *Store) CreateProvider(provider ProviderRecord) (ProviderRecord, error) {
	endpoints, err := normalizeProviderEndpoints(provider)
	if err != nil {
		return ProviderRecord{}, err
	}
	if provider.Name == "" {
		return ProviderRecord{}, errors.New("name is required")
	}
	now := storeNow()
	id, err := s.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		provider.Name, provider.PresetID, boolInt(provider.Enabled), now, now,
	)
	if err != nil {
		return ProviderRecord{}, err
	}
	if err := s.replaceProviderEndpoints(id, endpoints); err != nil {
		return ProviderRecord{}, err
	}
	return s.Provider(id)
}

func (s *Store) UpdateProvider(provider ProviderRecord) (ProviderRecord, error) {
	endpoints, err := normalizeProviderEndpoints(provider)
	if err != nil {
		return ProviderRecord{}, err
	}
	if provider.ID == 0 || provider.Name == "" {
		return ProviderRecord{}, errors.New("id and name are required")
	}
	_, err = s.exec(
		`UPDATE providers SET name = ?, preset_id = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		provider.Name, provider.PresetID, boolInt(provider.Enabled), storeNow(), provider.ID,
	)
	if err != nil {
		return ProviderRecord{}, err
	}
	if err := s.replaceProviderEndpoints(provider.ID, endpoints); err != nil {
		return ProviderRecord{}, err
	}
	return s.Provider(provider.ID)
}

func (s *Store) DeleteProvider(id int64) error {
	if id == 0 {
		return errors.New("id is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DELETE FROM model_routes WHERE provider_id = ?`,
		`DELETE FROM provider_keys WHERE provider_id = ?`,
		`DELETE FROM provider_endpoints WHERE provider_id = ?`,
		`DELETE FROM providers WHERE id = ?`,
	} {
		if _, err := tx.Exec(s.rebind(stmt), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ProviderKey(id int64, reveal bool) (ProviderKey, error) {
	keys, err := s.providerKeysByFilter(id, 0, reveal)
	if err != nil {
		return ProviderKey{}, err
	}
	if len(keys) == 0 {
		return ProviderKey{}, sql.ErrNoRows
	}
	return keys[0], nil
}

func (s *Store) ProviderKeys(providerID int64, reveal bool) ([]ProviderKey, error) {
	return s.providerKeysByFilter(0, providerID, reveal)
}

func (s *Store) providerKeysByFilter(id, providerID int64, reveal bool) ([]ProviderKey, error) {
	rows, err := s.query(`
		SELECT
			pk.id,
			pk.provider_id,
			p.name,
			pk.name,
			pk.secret,
			pk.secret_hint,
			pk.enabled,
			pk.priority,
			pk.weight,
			pk.labels,
			pk.disabled_status,
			pk.disabled_error_code,
			pk.disabled_error_message,
			pk.disabled_error_body,
			pk.disabled_at
		FROM provider_keys pk
		JOIN providers p ON p.id = pk.provider_id
		WHERE (? = 0 OR pk.id = ?)
			AND (? = 0 OR pk.provider_id = ?)
		ORDER BY p.name, pk.name`,
		id, id, providerID, providerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProviderKey, 0)
	for rows.Next() {
		var item ProviderKey
		var enabled int
		var disabledAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.ProviderID,
			&item.ProviderName,
			&item.Name,
			&item.Secret,
			&item.SecretHint,
			&enabled,
			&item.Priority,
			&item.Weight,
			&item.Labels,
			&item.DisabledStatus,
			&item.DisabledErrorCode,
			&item.DisabledMessage,
			&item.DisabledBody,
			&disabledAt,
		); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		item.DisabledAt = formatNullStoreTime(disabledAt)
		if !reveal {
			item.Secret = maskSecret(item.Secret)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateProviderKey(key ProviderKey) (ProviderKey, error) {
	if key.ProviderID == 0 || key.Secret == "" {
		return ProviderKey{}, errors.New("provider_id and secret are required")
	}
	if _, err := s.Provider(key.ProviderID); err != nil {
		return ProviderKey{}, err
	}
	if key.Name == "" {
		name, err := s.uniqueProviderKeyName(key.ProviderID)
		if err != nil {
			return ProviderKey{}, err
		}
		key.Name = name
	}
	if key.Weight == 0 {
		key.Weight = 1
	}
	now := storeNow()
	id, err := s.insertReturningID(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ProviderID, key.Name, key.Secret, secretHint(key.Secret), boolInt(key.Enabled), key.Priority, key.Weight, key.Labels, now, now,
	)
	if err != nil {
		return ProviderKey{}, err
	}
	return s.ProviderKey(id, false)
}

func (s *Store) UpdateProviderKey(key ProviderKey) (ProviderKey, error) {
	if key.ID == 0 || key.ProviderID == 0 || key.Name == "" {
		return ProviderKey{}, errors.New("id, provider_id and name are required")
	}
	if _, err := s.Provider(key.ProviderID); err != nil {
		return ProviderKey{}, err
	}
	if key.Weight == 0 {
		key.Weight = 1
	}
	now := storeNow()
	var err error
	if key.Secret == "" {
		_, err = s.exec(
			`UPDATE provider_keys SET provider_id = ?, name = ?, enabled = ?, priority = ?, weight = ?, labels = ?, disabled_status = CASE WHEN ? = 1 THEN 0 ELSE disabled_status END, disabled_error_code = CASE WHEN ? = 1 THEN '' ELSE disabled_error_code END, disabled_error_message = CASE WHEN ? = 1 THEN '' ELSE disabled_error_message END, disabled_error_body = CASE WHEN ? = 1 THEN '' ELSE disabled_error_body END, disabled_at = CASE WHEN ? = 1 THEN NULL ELSE disabled_at END, updated_at = ? WHERE id = ?`,
			key.ProviderID, key.Name, boolInt(key.Enabled), key.Priority, key.Weight, key.Labels, boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), now, key.ID,
		)
	} else {
		_, err = s.exec(
			`UPDATE provider_keys SET provider_id = ?, name = ?, secret = ?, secret_hint = ?, enabled = ?, priority = ?, weight = ?, labels = ?, disabled_status = CASE WHEN ? = 1 THEN 0 ELSE disabled_status END, disabled_error_code = CASE WHEN ? = 1 THEN '' ELSE disabled_error_code END, disabled_error_message = CASE WHEN ? = 1 THEN '' ELSE disabled_error_message END, disabled_error_body = CASE WHEN ? = 1 THEN '' ELSE disabled_error_body END, disabled_at = CASE WHEN ? = 1 THEN NULL ELSE disabled_at END, updated_at = ? WHERE id = ?`,
			key.ProviderID, key.Name, key.Secret, secretHint(key.Secret), boolInt(key.Enabled), key.Priority, key.Weight, key.Labels, boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), boolInt(key.Enabled), now, key.ID,
		)
	}
	if err != nil {
		return ProviderKey{}, err
	}
	return s.ProviderKey(key.ID, false)
}

func (s *Store) DeleteProviderKeys(ids []int64) error {
	if len(ids) == 0 {
		return errors.New("id is required")
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.exec(`DELETE FROM provider_keys WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return err
}

func (s *Store) Models() ([]Model, error) {
	rows, err := s.query(`
		SELECT id, name, display_name, icon, description, context_window, max_input_tokens, max_output_tokens,
			quota_uncached_input_rate, quota_cached_input_rate, quota_output_rate,
			capabilities, selection_policy, enabled, created_at, updated_at
		FROM models
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Model, 0)
	for rows.Next() {
		var item Model
		var enabled int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&item.ID, &item.Name, &item.DisplayName, &item.Icon, &item.Description,
			&item.ContextWindow, &item.MaxInputTokens, &item.MaxOutputTokens,
			&item.QuotaUncachedInputRate, &item.QuotaCachedInputRate, &item.QuotaOutputRate,
			&item.Capabilities, &item.SelectionPolicy, &enabled, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		item.CreatedAt = formatStoreTime(createdAt)
		item.UpdatedAt = formatStoreTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Model(id int64) (Model, error) {
	var item Model
	var enabled int
	var createdAt, updatedAt time.Time
	err := s.queryRow(`
		SELECT id, name, display_name, icon, description, context_window, max_input_tokens, max_output_tokens,
			quota_uncached_input_rate, quota_cached_input_rate, quota_output_rate,
			capabilities, selection_policy, enabled, created_at, updated_at
		FROM models
		WHERE id = ?`,
		id,
	).Scan(
		&item.ID, &item.Name, &item.DisplayName, &item.Icon, &item.Description,
		&item.ContextWindow, &item.MaxInputTokens, &item.MaxOutputTokens,
		&item.QuotaUncachedInputRate, &item.QuotaCachedInputRate, &item.QuotaOutputRate,
		&item.Capabilities, &item.SelectionPolicy, &enabled, &createdAt, &updatedAt,
	)
	item.Enabled = enabled != 0
	item.CreatedAt = formatStoreTime(createdAt)
	item.UpdatedAt = formatStoreTime(updatedAt)
	return item, err
}

func (s *Store) CreateModel(model Model) (Model, error) {
	if model.Name == "" {
		return Model{}, errors.New("name is required")
	}
	if model.Capabilities == "" {
		model.Capabilities = "{}"
	}
	if model.SelectionPolicy == "" {
		model.SelectionPolicy = "round_robin"
	}
	if err := validateModelQuotaRates(model); err != nil {
		return Model{}, err
	}
	now := storeNow()
	id, err := s.insertReturningID(
		`INSERT INTO models (name, display_name, icon, description, context_window, max_input_tokens, max_output_tokens, quota_uncached_input_rate, quota_cached_input_rate, quota_output_rate, capabilities, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		model.Name, model.DisplayName, model.Icon, model.Description, model.ContextWindow, model.MaxInputTokens, model.MaxOutputTokens, model.QuotaUncachedInputRate, model.QuotaCachedInputRate, model.QuotaOutputRate, model.Capabilities, model.SelectionPolicy, boolInt(model.Enabled), now, now,
	)
	if err != nil {
		return Model{}, err
	}
	return s.Model(id)
}

func (s *Store) UpdateModel(model Model) (Model, error) {
	if model.ID == 0 || model.Name == "" {
		return Model{}, errors.New("id and name are required")
	}
	if model.Capabilities == "" {
		model.Capabilities = "{}"
	}
	if model.SelectionPolicy == "" {
		model.SelectionPolicy = "round_robin"
	}
	if err := validateModelQuotaRates(model); err != nil {
		return Model{}, err
	}
	_, err := s.exec(
		`UPDATE models SET name = ?, display_name = ?, icon = ?, description = ?, context_window = ?, max_input_tokens = ?, max_output_tokens = ?, quota_uncached_input_rate = ?, quota_cached_input_rate = ?, quota_output_rate = ?, capabilities = ?, selection_policy = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		model.Name, model.DisplayName, model.Icon, model.Description, model.ContextWindow, model.MaxInputTokens, model.MaxOutputTokens, model.QuotaUncachedInputRate, model.QuotaCachedInputRate, model.QuotaOutputRate, model.Capabilities, model.SelectionPolicy, boolInt(model.Enabled), storeNow(), model.ID,
	)
	if err != nil {
		return Model{}, err
	}
	return s.Model(model.ID)
}

func validateModelQuotaRates(model Model) error {
	if math.IsNaN(model.QuotaUncachedInputRate) || math.IsInf(model.QuotaUncachedInputRate, 0) ||
		math.IsNaN(model.QuotaCachedInputRate) || math.IsInf(model.QuotaCachedInputRate, 0) ||
		math.IsNaN(model.QuotaOutputRate) || math.IsInf(model.QuotaOutputRate, 0) ||
		model.QuotaUncachedInputRate < 0 || model.QuotaCachedInputRate < 0 || model.QuotaOutputRate < 0 {
		return errors.New("quota rates must be non-negative")
	}
	return nil
}

func (s *Store) DeleteModel(id int64) error {
	if id == 0 {
		return errors.New("id is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.rebind(`DELETE FROM model_routes WHERE model_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM models WHERE id = ?`), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ModelRoutes(modelID int64) ([]ModelRoute, error) {
	rows, err := s.query(`
		SELECT
			mr.id,
			mr.model_id,
			p.id,
			p.name,
			mr.upstream_protocol,
			mr.upstream_model,
			mr.enabled,
			mr.priority,
			mr.weight
		FROM model_routes mr
		JOIN providers p ON p.id = mr.provider_id
		WHERE ? = 0 OR mr.model_id = ?
		ORDER BY mr.model_id, mr.priority DESC, mr.weight DESC, mr.id`,
		modelID, modelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ModelRoute, 0)
	for rows.Next() {
		var item ModelRoute
		var enabled int
		if err := rows.Scan(
			&item.ID,
			&item.ModelID,
			&item.ProviderID,
			&item.ProviderName,
			&item.UpstreamProtocol,
			&item.UpstreamModel,
			&enabled,
			&item.Priority,
			&item.Weight,
		); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ModelRoute(id int64) (ModelRoute, error) {
	routes, err := s.ModelRoutes(0)
	if err != nil {
		return ModelRoute{}, err
	}
	for _, route := range routes {
		if route.ID == id {
			return route, nil
		}
	}
	return ModelRoute{}, sql.ErrNoRows
}

// SaveModelRoute commits the route and optional imported model metadata together.
// The model remains the sole source of metadata after the import.
func (s *Store) SaveModelRoute(route ModelRoute, metadata *ModelMetadata) (ModelRoute, error) {
	route.UpstreamProtocol = normalizeRouteProtocol(route.UpstreamProtocol)
	if route.ModelID == 0 || route.ProviderID == 0 || route.UpstreamModel == "" {
		return ModelRoute{}, errors.New("model_id, provider_id and upstream_model are required")
	}
	if metadata != nil {
		if metadata.ContextWindow < 0 || metadata.MaxInputTokens < 0 || metadata.MaxOutputTokens < 0 {
			return ModelRoute{}, errors.New("model token limits must be non-negative")
		}
		if (metadata.QuotaUncachedInputRate != nil && *metadata.QuotaUncachedInputRate < 0) ||
			(metadata.QuotaCachedInputRate != nil && *metadata.QuotaCachedInputRate < 0) ||
			(metadata.QuotaOutputRate != nil && *metadata.QuotaOutputRate < 0) {
			return ModelRoute{}, errors.New("model quota rates must be non-negative")
		}
		for key := range metadata.Capabilities {
			switch key {
			case "stream", "vision", "audio_input", "tools", "reasoning", "json_schema":
			default:
				return ModelRoute{}, errors.New("unknown model capability: " + key)
			}
		}
	}
	if err := s.validateProviderRouteProtocol(route.ProviderID, route.UpstreamProtocol); err != nil {
		return ModelRoute{}, err
	}
	if route.Weight == 0 {
		route.Weight = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ModelRoute{}, err
	}
	defer tx.Rollback()
	var (
		currentCaps     string
		currentUncached float64
		currentCached   float64
		currentOutput   float64
	)
	if err := tx.QueryRow(s.rebind(`SELECT capabilities, quota_uncached_input_rate, quota_cached_input_rate, quota_output_rate FROM models WHERE id = ? FOR UPDATE`), route.ModelID).Scan(&currentCaps, &currentUncached, &currentCached, &currentOutput); err != nil {
		return ModelRoute{}, err
	}
	now := storeNow()
	if route.ID == 0 {
		err = tx.QueryRow(s.rebind(`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`),
			route.ModelID, route.ProviderID, route.UpstreamProtocol, route.UpstreamModel, boolInt(route.Enabled), route.Priority, route.Weight, now, now).Scan(&route.ID)
	} else {
		var result sql.Result
		result, err = tx.Exec(s.rebind(`UPDATE model_routes SET model_id = ?, provider_id = ?, upstream_protocol = ?, upstream_model = ?, enabled = ?, priority = ?, weight = ?, updated_at = ? WHERE id = ?`),
			route.ModelID, route.ProviderID, route.UpstreamProtocol, route.UpstreamModel, boolInt(route.Enabled), route.Priority, route.Weight, now, route.ID)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				err = sql.ErrNoRows
			}
		}
	}
	if err != nil {
		return ModelRoute{}, err
	}
	if metadata != nil {
		// Merge only supplied facts. False values overwrite true; unknown values do
		// not clear saved settings. Identity and selection policy stay local.
		caps := savedModelCapabilities(currentCaps)
		for key, value := range metadata.Capabilities {
			caps[key] = value
		}
		encoded, _ := json.Marshal(caps)

		uncachedRate := currentUncached
		if metadata.QuotaUncachedInputRate != nil {
			uncachedRate = *metadata.QuotaUncachedInputRate
		}
		cachedRate := currentCached
		if metadata.QuotaCachedInputRate != nil {
			cachedRate = *metadata.QuotaCachedInputRate
		}
		outputRate := currentOutput
		if metadata.QuotaOutputRate != nil {
			outputRate = *metadata.QuotaOutputRate
		}

		_, err = tx.Exec(s.rebind(`UPDATE models SET
   context_window = CASE WHEN ? > 0 THEN ? ELSE context_window END,
   max_input_tokens = CASE WHEN ? > 0 THEN ? ELSE max_input_tokens END,
   max_output_tokens = CASE WHEN ? > 0 THEN ? ELSE max_output_tokens END,
   quota_uncached_input_rate = ?,
   quota_cached_input_rate = ?,
   quota_output_rate = ?,
   capabilities = ?, updated_at = ? WHERE id = ?`),
			metadata.ContextWindow, metadata.ContextWindow,
			metadata.MaxInputTokens, metadata.MaxInputTokens,
			metadata.MaxOutputTokens, metadata.MaxOutputTokens,
			uncachedRate, cachedRate, outputRate,
			string(encoded), now, route.ModelID)
		if err != nil {
			return ModelRoute{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ModelRoute{}, err
	}
	return s.ModelRoute(route.ID)
}

func normalizeRouteProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return providerAuto
	}
	return protocol
}

func (s *Store) validateProviderRouteProtocol(providerID int64, protocol string) error {
	if _, err := s.Provider(providerID); err != nil {
		return err
	}
	if protocol == providerAuto {
		return nil
	}
	var exists bool
	if err := s.queryRow(
		`SELECT EXISTS(SELECT 1 FROM provider_endpoints WHERE provider_id = ? AND protocol = ?)`,
		providerID, protocol,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("provider does not support the selected protocol")
	}
	return nil
}

func (s *Store) DeleteModelRoute(id int64) error {
	if id == 0 {
		return errors.New("id is required")
	}
	_, err := s.exec(`DELETE FROM model_routes WHERE id = ?`, id)
	return err
}

func (s *Store) uniqueProviderKeyName(providerID int64) (string, error) {
	var providerName string
	if err := s.queryRow(`SELECT name FROM providers WHERE id = ?`, providerID).Scan(&providerName); err != nil {
		return "", err
	}
	prefix := providerKeyNamePrefix(providerName)
	for i := 0; i < 20; i++ {
		name := prefix + "-" + randomToken(5)
		var exists int
		err := s.queryRow(`SELECT COUNT(1) FROM provider_keys WHERE provider_id = ? AND name = ?`, providerID, name).Scan(&exists)
		if err != nil {
			return "", err
		}
		if exists == 0 {
			return name, nil
		}
	}
	return "", errors.New("failed to generate unique provider key name")
}

func providerKeyNamePrefix(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "pk"
	}
	if len(out) > 16 {
		return out[:16]
	}
	return out
}
