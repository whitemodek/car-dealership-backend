package inventory

import (
	"testing"
	"time"
)

func TestCarValidation(t *testing.T) {
	valid := CreateCar{ModelID: "00000000-0000-4000-8000-000000000001", VIN: "1HGCM82633A004352", Condition: "used", Year: 2023, MileageKM: 15000, PriceMinor: 250000000, Currency: "RUB", Color: "white", Fuel: "petrol", Transmission: "automatic"}
	if err := valid.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*CreateCar){
		func(c *CreateCar) { c.VIN = "1HGCM82633I004352" },
		func(c *CreateCar) { c.PriceMinor = 0 },
		func(c *CreateCar) { c.PriceMinor = 9007199254740992 },
		func(c *CreateCar) { c.Year = time.Now().Year() + 2 },
		func(c *CreateCar) { c.MileageKM = -1 },
		func(c *CreateCar) { c.Condition = "broken" },
		func(c *CreateCar) { c.Transmission = "anything" },
		func(c *CreateCar) { c.Photos = []string{"http://example.com/car.jpg"} },
	} {
		c := valid
		mutate(&c)
		if err := c.NormalizeAndValidate(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}
