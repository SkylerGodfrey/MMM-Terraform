# Variables for Magic Mirror Terraform configuration

variable "magicmirror_host" {
  description = "Hostname or IP address of the Magic Mirror device"
  type        = string
  default     = "192.168.1.50"
}

variable "magicmirror_port" {
  description = "Port of the Magic Mirror Agent API"
  type        = number
  default     = 8484
}

variable "magicmirror_api_key" {
  description = "API key for the Magic Mirror Agent"
  type        = string
  sensitive   = true
}

variable "weather_location" {
  description = "Location for weather forecasts (city name or coordinates)"
  type        = string
  default     = "New York"
}

variable "openweathermap_api_key" {
  description = "API key for OpenWeatherMap"
  type        = string
  sensitive   = true
}

variable "calendar_url" {
  description = "iCal URL for calendar events"
  type        = string
  default     = "https://calendar.google.com/calendar/ical/en.usa%23holiday%40group.v.calendar.google.com/public/basic.ics"
}
