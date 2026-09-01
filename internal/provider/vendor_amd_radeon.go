package provider

func init() {
	RegisterVendorAdapter(simpleVendorAdapter{
		name:    "amd-radeon",
		domains: []string{"developer.amd.com.cn"},
	})
}
