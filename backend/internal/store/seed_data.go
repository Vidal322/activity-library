package store

// Development sample data. IDs are fixed rather than generated so that
// re-seeding is a no-op, tests can reference known rows, and a demo link keeps
// working across a database reset.
//
// The prefix of each UUID identifies the table: 1 users, 2 category families,
// 3 categories, 4 locations, 5 materials, 6 games.

const (
	userAna   = "10000000-0000-7000-8000-000000000001"
	userBruno = "10000000-0000-7000-8000-000000000002"

	familyPurpose = "20000000-0000-7000-8000-000000000001"
	familyEnergy  = "20000000-0000-7000-8000-000000000002"

	catIcebreaker   = "30000000-0000-7000-8000-000000000001"
	catTeamBuilding = "30000000-0000-7000-8000-000000000002"
	catReflection   = "30000000-0000-7000-8000-000000000003"
	catCalm         = "30000000-0000-7000-8000-000000000004"
	catActive       = "30000000-0000-7000-8000-000000000005"

	locIndoor     = "40000000-0000-7000-8000-000000000001"
	locOutdoor    = "40000000-0000-7000-8000-000000000002"
	locSportsHall = "40000000-0000-7000-8000-000000000003"

	matRope      = "50000000-0000-7000-8000-000000000001"
	matBlindfold = "50000000-0000-7000-8000-000000000002"
	matBall      = "50000000-0000-7000-8000-000000000003"
	matPaper     = "50000000-0000-7000-8000-000000000004"

	gameHumanKnot      = "60000000-0000-7000-8000-000000000001"
	gameHumanKnotLarge = "60000000-0000-7000-8000-000000000002"
	gameBlindfoldMaze  = "60000000-0000-7000-8000-000000000003"
	gameCircleOfWords  = "60000000-0000-7000-8000-000000000004"
)

type seedUser struct {
	ID    string
	Name  string
	Email string
	Img   *string
}

type seedFamily struct {
	ID           string
	Name         string
	DisplayOrder int
}

type seedCategory struct {
	ID           string
	FamilyID     string
	Name         string
	Description  string
	Active       bool
	DisplayOrder int
}

type seedNamed struct {
	ID   string
	Name string
}

type seedMaterial struct {
	ID          string
	Name        string
	Description string
}

type seedGameMaterial struct {
	MaterialID     string
	Quantity       int
	PerParticipant bool
	Optional       bool
}

type seedBlock struct {
	ID       string
	Type     string
	Content  string
	Position int
}

type seedGame struct {
	ID              string
	Title           string
	Description     string
	AuthorID        string
	Image           *string
	MinParticipants *int
	MaxParticipants *int
	DurationMin     *int
	DurationMax     *int
	NoMaterials     bool
	PublishState    string
	OriginalID      *string

	Categories   []string
	Locations    []string
	Inspirations []string
	Materials    []seedGameMaterial
	Blocks       []seedBlock
}

func ptr[T any](v T) *T { return &v }

var seedUsers = []seedUser{
	{ID: userAna, Name: "Ana Marques", Email: "ana@example.com"},
	{ID: userBruno, Name: "Bruno Costa", Email: "bruno@example.com"},
}

var seedFamilies = []seedFamily{
	{ID: familyPurpose, Name: "Purpose", DisplayOrder: 0},
	{ID: familyEnergy, Name: "Energy", DisplayOrder: 1},
}

var seedCategories = []seedCategory{
	{
		ID:           catIcebreaker,
		FamilyID:     familyPurpose,
		Name:         "Icebreaker",
		Description:  "Helps a new group get talking.",
		Active:       true,
		DisplayOrder: 0,
	},
	{
		ID:           catTeamBuilding,
		FamilyID:     familyPurpose,
		Name:         "Team Building",
		Description:  "Builds trust and cooperation.",
		Active:       true,
		DisplayOrder: 1,
	},
	{
		ID:           catReflection,
		FamilyID:     familyPurpose,
		Name:         "Reflection",
		Description:  "Closes a session by looking back on it.",
		Active:       true,
		DisplayOrder: 2,
	},
	{
		ID:           catCalm,
		FamilyID:     familyEnergy,
		Name:         "Calm",
		Description:  "Low movement, suitable after a meal.",
		Active:       true,
		DisplayOrder: 0,
	},
	{
		ID:           catActive,
		FamilyID:     familyEnergy,
		Name:         "Active",
		Description:  "Raises the energy of the group.",
		Active:       true,
		DisplayOrder: 1,
	},
}

var seedLocations = []seedNamed{
	{ID: locIndoor, Name: "Indoor"},
	{ID: locOutdoor, Name: "Outdoor"},
	{ID: locSportsHall, Name: "Sports Hall"},
}

var seedMaterials = []seedMaterial{
	{ID: matRope, Name: "Rope", Description: "Roughly 10 metres, soft enough to hold."},
	{ID: matBlindfold, Name: "Blindfold", Description: "An opaque cloth or sleep mask."},
	{ID: matBall, Name: "Ball", Description: "Any soft ball that can be thrown indoors."},
	{ID: matPaper, Name: "Paper Sheet", Description: "A blank A4 sheet."},
}

var seedGames = []seedGame{
	{
		ID:              gameHumanKnot,
		Title:           "Human Knot",
		Description:     "The group tangles itself by joining hands across the circle, then untangles without letting go.",
		AuthorID:        userAna,
		MinParticipants: ptr(6),
		MaxParticipants: ptr(16),
		DurationMin:     ptr(10),
		DurationMax:     ptr(20),
		NoMaterials:     true,
		PublishState:    "published",
		Categories:      []string{catIcebreaker, catTeamBuilding, catActive},
		Locations:       []string{locIndoor, locOutdoor},
		Blocks: []seedBlock{
			{
				ID:       "70000000-0000-7000-8000-000000000001",
				Type:     "heading",
				Content:  "Setting up",
				Position: 0,
			},
			{
				ID:       "70000000-0000-7000-8000-000000000002",
				Type:     "paragraph",
				Content:  "Ask everyone to stand in a tight circle, shoulder to shoulder.",
				Position: 1,
			},
			{
				ID:       "70000000-0000-7000-8000-000000000003",
				Type:     "steps",
				Content:  "Reach across with your right hand and take someone else's hand.\nReach with your left and take a different person's hand.\nUntangle without releasing either grip.",
				Position: 2,
			},
			{
				ID:       "70000000-0000-7000-8000-000000000004",
				Type:     "note",
				Content:  "If the group cannot untangle after ten minutes, allow one pair to release and rejoin.",
				Position: 3,
			},
		},
	},
	{
		ID:           gameHumanKnotLarge,
		Title:        "Human Knot: Large Group",
		Description:  "Two knots race to untangle, then join into one.",
		AuthorID:     userAna,
		OriginalID:   ptr(gameHumanKnot),
		NoMaterials:  true,
		PublishState: "published",
		Categories:   []string{catTeamBuilding, catActive},
		Locations:    []string{locSportsHall},
	},
	{
		ID:              gameBlindfoldMaze,
		Title:           "Blindfold Maze",
		Description:     "Blindfolded players cross a rope maze guided only by a partner's voice.",
		AuthorID:        userBruno,
		MinParticipants: ptr(4),
		MaxParticipants: ptr(20),
		DurationMin:     ptr(20),
		DurationMax:     ptr(40),
		PublishState:    "published",
		Categories:      []string{catTeamBuilding, catActive},
		Locations:       []string{locOutdoor, locSportsHall},
		Inspirations:    []string{gameHumanKnot},
		Materials: []seedGameMaterial{
			{MaterialID: matBlindfold, Quantity: 1, PerParticipant: true},
			{MaterialID: matRope, Quantity: 2},
			{MaterialID: matBall, Quantity: 1, Optional: true},
		},
		Blocks: []seedBlock{
			{
				ID:       "70000000-0000-7000-8000-000000000005",
				Type:     "heading",
				Content:  "Before you start",
				Position: 0,
			},
			{
				ID:       "70000000-0000-7000-8000-000000000006",
				Type:     "bullet_points",
				Content:  "Lay the rope out as a winding path.\nPair players up.\nAgree a signal that stops the game at once.",
				Position: 1,
			},
			{
				ID:       "70000000-0000-7000-8000-000000000007",
				Type:     "paragraph",
				Content:  "One partner is blindfolded; the other may only speak, never touch.",
				Position: 2,
			},
		},
	},
	{
		ID:              gameCircleOfWords,
		Title:           "Circle of Words",
		Description:     "A closing round where each person names one word for how the day went.",
		AuthorID:        userBruno,
		MinParticipants: ptr(3),
		MaxParticipants: ptr(30),
		DurationMin:     ptr(5),
		DurationMax:     ptr(15),
		// Left as a draft so the listing has something unpublished to filter.
		PublishState: "draft",
		Categories:   []string{catReflection, catCalm},
		Locations:    []string{locIndoor},
		Materials: []seedGameMaterial{
			{MaterialID: matPaper, Quantity: 1, PerParticipant: true, Optional: true},
		},
		Blocks: []seedBlock{
			{
				ID:       "70000000-0000-7000-8000-000000000008",
				Type:     "paragraph",
				Content:  "Sit in a circle. Going clockwise, each person says one word and nothing more.",
				Position: 0,
			},
		},
	},
}
