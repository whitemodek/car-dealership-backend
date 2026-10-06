package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whitemodek/car-dealership-backend/backend/internal/domain"
)

type Store struct{ Pool *pgxpool.Pool }

type Model struct {
	ID   string `json:"id"`
	Make string `json:"make"`
	Name string `json:"name"`
}
type Trim struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
}
type Car struct {
	ID           string    `json:"id"`
	ModelID      string    `json:"model_id"`
	TrimID       *string   `json:"trim_id"`
	Make         string    `json:"make"`
	Model        string    `json:"model"`
	Trim         *string   `json:"trim"`
	VIN          string    `json:"vin"`
	Condition    string    `json:"condition"`
	Year         int       `json:"year"`
	MileageKM    int       `json:"mileage_km"`
	PriceMinor   int64     `json:"price_minor"`
	Currency     string    `json:"currency"`
	Color        string    `json:"color"`
	Fuel         string    `json:"fuel"`
	Transmission string    `json:"transmission"`
	Description  string    `json:"description"`
	Publication  string    `json:"publication"`
	SaleStatus   string    `json:"sale_status"`
	Available    bool      `json:"available"`
	Version      int       `json:"version"`
	Photos       []string  `json:"photos"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type CreateCar struct {
	ModelID      string   `json:"model_id"`
	TrimID       *string  `json:"trim_id"`
	VIN          string   `json:"vin"`
	Condition    string   `json:"condition"`
	Year         int      `json:"year"`
	MileageKM    int      `json:"mileage_km"`
	PriceMinor   int64    `json:"price_minor"`
	Currency     string   `json:"currency"`
	Color        string   `json:"color"`
	Fuel         string   `json:"fuel"`
	Transmission string   `json:"transmission"`
	Description  string   `json:"description"`
	Photos       []string `json:"photos"`
}
type UpdateCar struct {
	Version     int       `json:"version"`
	PriceMinor  *int64    `json:"price_minor"`
	Publication *string   `json:"publication"`
	SaleStatus  *string   `json:"sale_status"`
	Description *string   `json:"description"`
	Color       *string   `json:"color"`
	Photos      *[]string `json:"photos"`
}
type Filter struct {
	Limit, Offset                             int
	Query, Condition, ModelID, Currency, Sort string
	MinPrice, MaxPrice                        int64
}

var vinPattern = regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func ValidText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len([]rune(value)) <= max && !strings.ContainsRune(value, '\x00')
}
func ValidPhotos(photos []string) bool {
	if len(photos) > 20 {
		return false
	}
	for _, photo := range photos {
		u, err := url.Parse(photo)
		if err != nil || len(photo) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return false
		}
	}
	return true
}
func (in *CreateCar) NormalizeAndValidate() error {
	in.VIN = strings.ToUpper(strings.TrimSpace(in.VIN))
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.Color = strings.TrimSpace(in.Color)
	if !domain.ValidID(in.ModelID) || (in.TrimID != nil && !domain.ValidID(*in.TrimID)) || !vinPattern.MatchString(in.VIN) || !currencyPattern.MatchString(in.Currency) ||
		(in.Condition != "new" && in.Condition != "used") || in.Year < 1886 || in.Year > time.Now().UTC().Year()+1 || in.MileageKM < 0 || in.MileageKM > 2000000 ||
		in.PriceMinor <= 0 || in.PriceMinor > 9007199254740991 || !ValidText(in.Color, 80) || len([]rune(in.Description)) > 10000 || strings.ContainsRune(in.Description, '\x00') ||
		!oneOf(in.Fuel, "petrol", "diesel", "hybrid", "electric") || !oneOf(in.Transmission, "manual", "automatic") || !ValidPhotos(in.Photos) {
		return domain.ErrInvalid
	}
	return nil
}
func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

func (s Store) Models(ctx context.Context) ([]Model, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,make,name FROM models ORDER BY make,name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Model{}
	for rows.Next() {
		var v Model
		if err := rows.Scan(&v.ID, &v.Make, &v.Name); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s Store) Trims(ctx context.Context, model string) ([]Trim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text,model_id::text,name FROM trims WHERE model_id=$1 ORDER BY name,id`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Trim{}
	for rows.Next() {
		var v Trim
		if err := rows.Scan(&v.ID, &v.ModelID, &v.Name); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (s Store) CreateModel(ctx context.Context, actor, makeName, name string) (Model, error) {
	v := Model{Make: strings.TrimSpace(makeName), Name: strings.TrimSpace(name)}
	if !ValidText(v.Make, 80) || !ValidText(v.Name, 80) {
		return v, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `INSERT INTO models(make,name) VALUES($1,$2) RETURNING id::text`, v.Make, v.Name).Scan(&v.ID); err != nil {
		return v, err
	}
	if err = domain.Audit(ctx, tx, actor, "model.created", v.ID, map[string]any{}); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}
func (s Store) CreateTrim(ctx context.Context, actor, model, name string) (Trim, error) {
	v := Trim{ModelID: model, Name: strings.TrimSpace(name)}
	if !domain.ValidID(model) || !ValidText(v.Name, 80) {
		return v, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `INSERT INTO trims(model_id,name) VALUES($1,$2) RETURNING id::text`, model, v.Name).Scan(&v.ID); err != nil {
		return v, err
	}
	if err = domain.Audit(ctx, tx, actor, "trim.created", v.ID, map[string]any{}); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}

const carSelect = `SELECT c.id::text,c.model_id::text,c.trim_id::text,m.make,m.name,t.name,c.vin,c.condition,c.year,c.mileage_km,c.price_minor,c.currency,c.color,c.fuel,c.transmission,c.description,c.publication,c.sale_status,
 c.publication='published' AND c.sale_status='available' AND NOT EXISTS(SELECT 1 FROM reservations r WHERE r.car_id=c.id AND r.status IN ('active','confirmed') AND r.expires_at>clock_timestamp()),
 c.version,COALESCE((SELECT jsonb_agg(p.url ORDER BY p.position) FROM car_photos p WHERE p.car_id=c.id),'[]'::jsonb),c.created_at,c.updated_at
 FROM cars c JOIN models m ON m.id=c.model_id LEFT JOIN trims t ON t.id=c.trim_id `

func scanCar(row pgx.Row) (Car, error) {
	var c Car
	var photos []byte
	err := row.Scan(&c.ID, &c.ModelID, &c.TrimID, &c.Make, &c.Model, &c.Trim, &c.VIN, &c.Condition, &c.Year, &c.MileageKM, &c.PriceMinor, &c.Currency, &c.Color, &c.Fuel, &c.Transmission, &c.Description, &c.Publication, &c.SaleStatus, &c.Available, &c.Version, &photos, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(photos, &c.Photos); err != nil {
		return c, err
	}
	return c, nil
}
func (s Store) Get(ctx context.Context, id string, staff bool) (Car, error) {
	return scanCar(s.Pool.QueryRow(ctx, carSelect+` WHERE c.id=$1 AND ($2 OR (c.publication='published' AND c.sale_status='available'))`, id, staff))
}
func (s Store) List(ctx context.Context, f Filter, staff bool) ([]Car, error) {
	order := "c.created_at DESC,c.id DESC"
	switch f.Sort {
	case "price_asc":
		order = "c.price_minor ASC,c.id ASC"
	case "price_desc":
		order = "c.price_minor DESC,c.id DESC"
	case "year_desc":
		order = "c.year DESC,c.id DESC"
	case "", "newest":
	default:
		return nil, domain.ErrInvalid
	}
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 || f.Offset > 100000 || len([]rune(f.Query)) > 100 || strings.ContainsRune(f.Query, '\x00') ||
		(f.Condition != "" && !oneOf(f.Condition, "new", "used")) || (f.ModelID != "" && !domain.ValidID(f.ModelID)) ||
		(f.Currency != "" && !currencyPattern.MatchString(f.Currency)) || f.MinPrice < 0 || f.MaxPrice < 0 || (f.MaxPrice > 0 && f.MinPrice > f.MaxPrice) ||
		((f.MinPrice > 0 || f.MaxPrice > 0 || strings.HasPrefix(f.Sort, "price_")) && f.Currency == "") {
		return nil, domain.ErrInvalid
	}
	var model any
	if f.ModelID != "" {
		model = f.ModelID
	}
	query := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Query)
	rows, err := s.Pool.Query(ctx, carSelect+` WHERE ($1 OR (c.publication='published' AND c.sale_status='available'))
 AND ($2='' OR c.condition=$2) AND ($3::uuid IS NULL OR c.model_id=$3) AND ($4='' OR c.currency=$4)
 AND ($5::bigint=0 OR c.price_minor>=$5) AND ($6::bigint=0 OR c.price_minor<=$6)
 AND ($7='' OR m.make ILIKE '%' || $7 || '%' OR m.name ILIKE '%' || $7 || '%') ORDER BY `+order+` LIMIT $8 OFFSET $9`, staff, f.Condition, model, f.Currency, f.MinPrice, f.MaxPrice, query, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Car{}
	for rows.Next() {
		c, err := scanCar(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s Store) Create(ctx context.Context, actor string, in CreateCar) (Car, error) {
	if err := in.NormalizeAndValidate(); err != nil {
		return Car{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Car{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO cars(model_id,trim_id,vin,condition,year,mileage_km,price_minor,currency,color,fuel,transmission,description)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id::text`, in.ModelID, in.TrimID, in.VIN, in.Condition, in.Year, in.MileageKM, in.PriceMinor, in.Currency, in.Color, in.Fuel, in.Transmission, in.Description).Scan(&id)
	if err != nil {
		return Car{}, err
	}
	if err = replacePhotos(ctx, tx, id, in.Photos); err != nil {
		return Car{}, err
	}
	if err = domain.Audit(ctx, tx, actor, "car.created", id, map[string]any{}); err != nil {
		return Car{}, err
	}
	car, err := scanCar(tx.QueryRow(ctx, carSelect+` WHERE c.id=$1`, id))
	if err != nil {
		return Car{}, err
	}
	return car, tx.Commit(ctx)
}
func replacePhotos(ctx context.Context, tx pgx.Tx, id string, photos []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM car_photos WHERE car_id=$1`, id); err != nil {
		return err
	}
	for i, photo := range photos {
		if _, err := tx.Exec(ctx, `INSERT INTO car_photos(car_id,url,position) VALUES($1,$2,$3)`, id, photo, i); err != nil {
			return err
		}
	}
	return nil
}
func (s Store) Update(ctx context.Context, actor, id string, in UpdateCar) (Car, error) {
	if in.Version < 1 || (in.PriceMinor == nil && in.Publication == nil && in.SaleStatus == nil && in.Description == nil && in.Color == nil && in.Photos == nil) ||
		(in.PriceMinor != nil && (*in.PriceMinor <= 0 || *in.PriceMinor > 9007199254740991)) ||
		(in.Publication != nil && !oneOf(*in.Publication, "draft", "published", "archived")) || (in.SaleStatus != nil && !oneOf(*in.SaleStatus, "available", "sold", "withdrawn")) ||
		(in.Description != nil && (len([]rune(*in.Description)) > 10000 || strings.ContainsRune(*in.Description, '\x00'))) || (in.Color != nil && !ValidText(*in.Color, 80)) ||
		(in.Photos != nil && !ValidPhotos(*in.Photos)) {
		return Car{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Car{}, err
	}
	defer tx.Rollback(ctx)
	car, err := scanCar(tx.QueryRow(ctx, carSelect+` WHERE c.id=$1 FOR UPDATE OF c`, id))
	if err != nil {
		return Car{}, err
	}
	if car.Version != in.Version {
		return Car{}, domain.ErrConflict
	}
	if car.SaleStatus == "sold" && in.SaleStatus != nil && *in.SaleStatus != "sold" {
		return Car{}, domain.ErrConflict
	}
	if in.PriceMinor != nil && car.PriceMinor != *in.PriceMinor {
		if _, err = tx.Exec(ctx, `INSERT INTO price_history(car_id,old_price_minor,new_price_minor,currency,actor_id) VALUES($1,$2,$3,$4,$5)`, id, car.PriceMinor, *in.PriceMinor, car.Currency, actor); err != nil {
			return Car{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE cars SET price_minor=COALESCE($2,price_minor),publication=COALESCE($3,publication),sale_status=COALESCE($4,sale_status),description=COALESCE($5,description),color=COALESCE($6,color),version=version+1,updated_at=now() WHERE id=$1`, id, in.PriceMinor, in.Publication, in.SaleStatus, in.Description, in.Color)
	if err != nil {
		return Car{}, err
	}
	if in.Photos != nil {
		if err = replacePhotos(ctx, tx, id, *in.Photos); err != nil {
			return Car{}, err
		}
	}
	updated, err := scanCar(tx.QueryRow(ctx, carSelect+` WHERE c.id=$1`, id))
	if err != nil {
		return Car{}, err
	}
	if updated.SaleStatus != "available" || updated.Publication != "published" {
		status := "cancelled"
		if updated.SaleStatus == "sold" {
			status = "completed"
		}
		rows, err := tx.Query(ctx, `UPDATE reservations SET status=CASE WHEN expires_at<=now() THEN 'expired' ELSE $2 END,updated_at=now() WHERE car_id=$1 AND status IN ('active','confirmed') RETURNING id::text,status`, id, status)
		if err != nil {
			return Car{}, err
		}
		type change struct{ id, status string }
		changes := []change{}
		for rows.Next() {
			var v change
			if err = rows.Scan(&v.id, &v.status); err != nil {
				rows.Close()
				return Car{}, err
			}
			changes = append(changes, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return Car{}, err
		}
		for _, v := range changes {
			if err = domain.Audit(ctx, tx, actor, "reservation."+v.status, v.id, map[string]any{}); err != nil {
				return Car{}, err
			}
			if err = domain.Event(ctx, tx, "reservation."+v.status, v.id); err != nil {
				return Car{}, err
			}
		}
	}
	if err = domain.Audit(ctx, tx, actor, "car.updated", id, map[string]any{"version": updated.Version, "price_minor": updated.PriceMinor, "publication": updated.Publication, "sale_status": updated.SaleStatus}); err != nil {
		return Car{}, err
	}
	if err = domain.Event(ctx, tx, "car.updated", id); err != nil {
		return Car{}, err
	}
	return updated, tx.Commit(ctx)
}
func (s Store) PriceHistory(ctx context.Context, id string) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT old_price_minor,new_price_minor,currency,created_at FROM price_history WHERE car_id=$1 ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var old, new int64
		var currency string
		var at time.Time
		if err = rows.Scan(&old, &new, &currency, &at); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"old_price_minor": old, "new_price_minor": new, "currency": currency, "created_at": at})
	}
	return items, rows.Err()
}
