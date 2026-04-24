message(STATUS "Building s3 proxy client library")

add_subdirectory(proto)

add_library(s3_proxy STATIC
  clnt.cc
)

target_link_libraries(s3_proxy PUBLIC
  serr
  util_log
  rpc
  rpc_spchannel
  sigmap_proxy
  s3_proto
)

target_include_directories(s3_proxy PUBLIC ${CMAKE_SOURCE_DIR})
