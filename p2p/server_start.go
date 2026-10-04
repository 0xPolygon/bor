package p2p

// Accept loops start only after setup succeeds, so rollback cannot wait on
// handshakes that need Start's server lock.
func (srv *Server) startLoops() {
	if srv.listener != nil {
		srv.loopWG.Add(1)
		go srv.listenLoop()
	}
	if srv.bulk != nil {
		srv.loopWG.Add(1)
		go func() {
			defer srv.loopWG.Done()
			srv.bulk.run()
		}()
	}
	srv.loopWG.Add(1)
	go srv.run()
}

// rollbackStart runs under srv.lock, before the main loop owns these resources.
func (srv *Server) rollbackStart() {
	srv.running = false
	if srv.quit != nil {
		close(srv.quit)
	}
	if srv.listener != nil {
		if err := srv.listener.Close(); err != nil {
			srv.log.Debug("Listener cleanup failed", "err", err)
		}
	}
	if srv.bulk != nil {
		srv.bulk.Close()
	}
	if srv.discv4 != nil {
		srv.discv4.Close()
	}
	if srv.discv5 != nil {
		srv.discv5.Close()
	}
	srv.loopWG.Wait()
	if srv.discmix != nil {
		srv.discmix.Close()
	}
	if srv.nodedb != nil {
		srv.nodedb.Close()
	}
	srv.listener, srv.bulk, srv.nodedb, srv.localnode = nil, nil, nil, nil
	srv.discv4, srv.discv5, srv.discmix = nil, nil, nil
}
