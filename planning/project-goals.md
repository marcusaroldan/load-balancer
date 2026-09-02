
The goal of this project is to create a full load balancer system that is able to be configured dynamically, respond to changing network conditions, request volume, etc.
The load balancer will be written in Golang for the components
To display and showcase this project, I want to be able to launch a sandbox/simulator that runs the load balancer, with interactions to change the network conditions, add new servers or pools, create a server failure, network partition, increase the request volume, change the algorithm, add new load balancer nodes, etc.
This sandbox/simulator should be able to be deployed for low cost on my portfolio website in some fashion. This will probably be a dedicated page/endpoint with a custom frontend created.