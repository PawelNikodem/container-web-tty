package route

type InitMessage struct {
	Arguments string `json:"Arguments,omitempty"`
	AuthToken string `json:"AuthToken,omitempty"`
}
