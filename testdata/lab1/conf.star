# Simple router + 2 hosts lab

datanet = Subnet(network="172.16.0.0/20")
hostnet = Subnet(network="172.16.16.0/24", host=True)

internet = Outnet(link="wlp10s0")

r1 = Router("mrt")
r1.attach_nic(datanet, addr=datanet.addr(1))
r1.attach_nic(hostnet, addr=hostnet.addr(2))
r1.init_script = """
{{ range .Interfaces }}
/ip/address/add interface={{.Name}} address={{.Address}}/24
{{ end }}

/ip/pool
add ranges=172.16.0.10-172.16.0.20

/ip/dhcp-server/network
add address=172.16.0/24 gateway=172.16.0.1 dns-server=172.16.0.1

/ip/dhcp-server
add address-pool=pool0 interface=ether2
"""


d1 = Host()
d1.attach_nic(internet)

#d2 = Host()
#d2.attach_nic(datanet)