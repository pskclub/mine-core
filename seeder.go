package core

type ISeed interface {
	Run() error
}

type Seeder struct {
}

func NewSeeder() *Seeder {
	return &Seeder{}
}

func (receiver Seeder) Add(seeder ISeed) error {
	return seeder.Run()
}
